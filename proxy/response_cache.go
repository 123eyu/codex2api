package proxy

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/codex2api/cache"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ==================== 响应上下文缓存 ====================
// 用于解决 previous_response_id 场景下 tool calling 上下文丢失的问题。
// 代理层设置 store=false 并删除 previous_response_id，导致上游无法恢复历史 function_call。
// 本模块在本地缓存每次响应的累积对话上下文，当下一个请求带 previous_response_id 时，
// 自动将历史 items 注入回 input，使上游无需依赖服务端存储即可匹配 call_id。
//
// 隔离：缓存键 = owner(下游 API Key ID) + response_id。没有 owner 维度时，任何请求
// 只要带上（猜到/复用了）别人的 response_id 就能把别人的对话历史注入自己的 input，
// 造成跨用户上下文泄露。owner 不匹配一律按缓存未命中处理。

const (
	responseCacheTTL        = 10 * time.Minute
	responseCacheMaxBytes   = 64 << 20
	responseCacheMaxEntry   = 8 << 20
	responseCacheMaxItems   = 2000 // 缓存条目上限，防止内存膨胀
	responseCacheMaxPerItem = 200  // 单条缓存最大 raw items 数
	responseCleanupInterval = 2 * time.Minute
)

// responseCacheOwner 生成响应上下文缓存的归属命名空间。
// 以下游 API Key ID 为主；无鉴权上下文(0)时退化到共享匿名空间（仅测试/内嵌场景）。
func responseCacheOwner(apiKeyID int64) string {
	if apiKeyID > 0 {
		return fmt.Sprintf("key:%d", apiKeyID)
	}
	return "anon"
}

// responseCacheStoreKey 组合 owner 与 response_id 作为缓存键（本地 map 与 Redis 共用）。
func responseCacheStoreKey(owner, responseID string) string {
	if owner == "" {
		owner = "anon"
	}
	return owner + "|" + responseID
}

type responseCacheEntry struct {
	key       string
	items     []json.RawMessage
	bytes     int64
	expiresAt time.Time
	element   *list.Element
}

type responseCacheConfig struct {
	maxBytes      int64
	maxEntryBytes int64
	maxEntries    int
	ttl           time.Duration
	maxItems      int
}

func defaultResponseCacheConfig() responseCacheConfig {
	return responseCacheConfig{
		maxBytes:      responseCacheMaxBytes,
		maxEntryBytes: responseCacheMaxEntry,
		maxEntries:    responseCacheMaxItems,
		ttl:           responseCacheTTL,
		maxItems:      responseCacheMaxPerItem,
	}
}

// ResponseCacheStats is a point-in-time snapshot of the bounded local cache.
// Bytes are logical retained JSON payload bytes; they are not an RSS estimate.
type ResponseCacheStats struct {
	Entries               int
	Bytes                 int64
	HighWaterBytes        int64
	LargestSeenEntryBytes int64
	Hits                  uint64
	Misses                uint64
	Expirations           uint64
	CountEvictions        uint64
	ByteEvictions         uint64
	OversizeSkips         uint64
}

type responseCacheState struct {
	mu           sync.RWMutex
	store        map[string]*responseCacheEntry
	lru          *list.List
	config       responseCacheConfig
	stats        ResponseCacheStats
	runtimeCache cache.TokenCache
}

var respCache responseCacheState

func init() {
	respCache.store = make(map[string]*responseCacheEntry)
	respCache.lru = list.New()
	respCache.config = defaultResponseCacheConfig()
	go respCacheCleanupLoop()
}

func SetResponseContextCache(tc cache.TokenCache) {
	respCache.mu.Lock()
	respCache.runtimeCache = tc
	respCache.mu.Unlock()
}

// GetResponseCacheStats returns a thread-safe local-cache snapshot.
func GetResponseCacheStats() ResponseCacheStats {
	respCache.mu.RLock()
	stats := respCache.stats
	respCache.mu.RUnlock()
	return stats
}

// resetResponseCacheStateForTest replaces all local state with a deterministic
// test configuration. It deliberately remains package-private.
func resetResponseCacheStateForTest(config responseCacheConfig) {
	respCache.mu.Lock()
	respCache.store = make(map[string]*responseCacheEntry)
	respCache.lru = list.New()
	respCache.config = config
	respCache.stats = ResponseCacheStats{}
	respCache.runtimeCache = nil
	respCache.mu.Unlock()
}

// configureResponseCacheForTest exercises the same immediate runtime-shrink
// path future configuration wiring will use, without exposing configuration.
func configureResponseCacheForTest(config responseCacheConfig) {
	respCache.mu.Lock()
	respCache.config = config
	respCache.enforceConfigLocked()
	respCache.mu.Unlock()
}

// setResponseCache 存储响应上下文（按 owner 命名空间隔离）
func setResponseCache(owner, responseID string, items []json.RawMessage) {
	storeKey := responseCacheStoreKey(owner, responseID)
	itemsCopy, admitted := admitResponseCache(storeKey, items)
	if !admitted {
		return
	}

	respCache.mu.RLock()
	runtimeCache := respCache.runtimeCache
	respCache.mu.RUnlock()

	if runtimeCache != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := runtimeCache.SetResponseContext(ctx, storeKey, itemsCopy, responseCacheTTL); err != nil {
			log.Printf("写入 Redis response context 失败: response_id=%s err=%v", responseID, err)
		}
	}
}

func admitResponseCache(storeKey string, items []json.RawMessage) ([]json.RawMessage, bool) {
	respCache.mu.Lock()
	defer respCache.mu.Unlock()

	items = trimResponseContextTail(items, respCache.config.maxItems)
	var entryBytes int64
	for _, item := range items {
		entryBytes += int64(len(item))
	}
	if entryBytes > respCache.stats.LargestSeenEntryBytes {
		respCache.stats.LargestSeenEntryBytes = entryBytes
	}
	if respCache.config.maxEntries <= 0 ||
		entryBytes > respCache.config.maxEntryBytes ||
		entryBytes > respCache.config.maxBytes {
		respCache.stats.OversizeSkips++
		return nil, false
	}

	itemsCopy := make([]json.RawMessage, len(items))
	for i, item := range items {
		itemsCopy[i] = append(json.RawMessage(nil), item...)
	}
	if existing := respCache.store[storeKey]; existing != nil {
		respCache.removeEntryLocked(existing, responseCacheRemovalReplace)
	}
	for len(respCache.store)+1 > respCache.config.maxEntries ||
		respCache.stats.Bytes+entryBytes > respCache.config.maxBytes {
		oldest := respCache.oldestLocked()
		if oldest == nil {
			respCache.stats.OversizeSkips++
			return nil, false
		}
		reason := responseCacheRemovalByteEviction
		if len(respCache.store)+1 > respCache.config.maxEntries {
			reason = responseCacheRemovalCountEviction
		}
		respCache.removeEntryLocked(oldest, reason)
	}

	entry := &responseCacheEntry{
		key:       storeKey,
		items:     itemsCopy,
		bytes:     entryBytes,
		expiresAt: time.Now().Add(respCache.config.ttl),
	}
	entry.element = respCache.lru.PushFront(entry)
	respCache.store[storeKey] = entry
	respCache.stats.Entries = len(respCache.store)
	respCache.stats.Bytes += entryBytes
	if respCache.stats.Bytes > respCache.stats.HighWaterBytes {
		respCache.stats.HighWaterBytes = respCache.stats.Bytes
	}
	return itemsCopy, true
}

type responseCacheRemovalReason uint8

const (
	responseCacheRemovalReplace responseCacheRemovalReason = iota
	responseCacheRemovalExpiration
	responseCacheRemovalCountEviction
	responseCacheRemovalByteEviction
)

func (c *responseCacheState) oldestLocked() *responseCacheEntry {
	if c.lru == nil {
		return nil
	}
	element := c.lru.Back()
	if element == nil {
		return nil
	}
	entry, _ := element.Value.(*responseCacheEntry)
	return entry
}

func (c *responseCacheState) removeEntryLocked(entry *responseCacheEntry, reason responseCacheRemovalReason) {
	if entry == nil || c.store[entry.key] != entry {
		return
	}
	delete(c.store, entry.key)
	if entry.element != nil {
		c.lru.Remove(entry.element)
	}
	c.stats.Entries = len(c.store)
	c.stats.Bytes -= entry.bytes
	switch reason {
	case responseCacheRemovalExpiration:
		c.stats.Expirations++
	case responseCacheRemovalCountEviction:
		c.stats.CountEvictions++
	case responseCacheRemovalByteEviction:
		c.stats.ByteEvictions++
	}
}

func (c *responseCacheState) enforceConfigLocked() {
	for element := c.lru.Back(); element != nil; {
		previous := element.Prev()
		entry, _ := element.Value.(*responseCacheEntry)
		if entry != nil && entry.bytes > c.config.maxEntryBytes {
			c.removeEntryLocked(entry, responseCacheRemovalByteEviction)
		}
		element = previous
	}
	for len(c.store) > c.config.maxEntries || c.stats.Bytes > c.config.maxBytes {
		oldest := c.oldestLocked()
		if oldest == nil {
			break
		}
		reason := responseCacheRemovalByteEviction
		if len(c.store) > c.config.maxEntries {
			reason = responseCacheRemovalCountEviction
		}
		c.removeEntryLocked(oldest, reason)
	}
}

// trimResponseContextTail keeps one contiguous suffix no longer than maxItems.
// If the initial boundary splits a known call/output pair, the whole crossing
// group is excluded. Moving the boundary can expose another crossing group, so
// the scan repeats until every retained pair is complete.
func trimResponseContextTail(items []json.RawMessage, maxItems int) []json.RawMessage {
	if maxItems <= 0 {
		return nil
	}
	if len(items) <= maxItems {
		return items
	}

	type callGroup struct {
		first     int
		last      int
		hasCall   bool
		hasOutput bool
	}
	groups := make(map[string]callGroup)
	for index, item := range items {
		typ := gjson.GetBytes(item, "type").String()
		isCall := isCodexToolCallContextType(typ)
		isOutput := isCodexToolCallOutputType(typ)
		if !isCall && !isOutput {
			continue
		}
		callID := gjson.GetBytes(item, "call_id").String()
		if callID == "" {
			continue
		}
		group, exists := groups[callID]
		if !exists {
			group.first = index
		}
		group.last = index
		group.hasCall = group.hasCall || isCall
		group.hasOutput = group.hasOutput || isOutput
		groups[callID] = group
	}

	start := len(items) - maxItems
	for {
		nextStart := start
		for _, group := range groups {
			if !group.hasCall || !group.hasOutput {
				continue
			}
			if group.first < start && group.last >= start && group.last+1 > nextStart {
				nextStart = group.last + 1
			}
		}
		if nextStart == start {
			return items[start:]
		}
		start = nextStart
	}
}

// getResponseCache 查找缓存的响应上下文；owner 不匹配等同缓存未命中，
// 防止跨 API Key 用 response_id 拉取他人对话历史。
func getResponseCache(owner, responseID string) []json.RawMessage {
	storeKey := responseCacheStoreKey(owner, responseID)
	respCache.mu.Lock()
	entry, ok := respCache.store[storeKey]
	runtimeCache := respCache.runtimeCache
	if ok {
		if !time.Now().Before(entry.expiresAt) {
			respCache.removeEntryLocked(entry, responseCacheRemovalExpiration)
		} else {
			respCache.lru.MoveToFront(entry.element)
			respCache.stats.Hits++
			respCache.mu.Unlock()
			return entry.items
		}
	}
	respCache.stats.Misses++
	respCache.mu.Unlock()

	if runtimeCache == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	items, err := runtimeCache.GetResponseContext(ctx, storeKey)
	if err != nil {
		log.Printf("读取 Redis response context 失败: response_id=%s err=%v", responseID, err)
		return nil
	}
	if len(items) == 0 {
		return nil
	}
	items, admitted := setResponseCacheLocal(storeKey, items)
	if !admitted {
		return nil
	}
	return items
}

func setResponseCacheLocal(storeKey string, items []json.RawMessage) ([]json.RawMessage, bool) {
	return admitResponseCache(storeKey, items)
}

// respCacheCleanupLoop 后台清理过期条目
func respCacheCleanupLoop() {
	ticker := time.NewTicker(responseCleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		respCache.mu.Lock()
		for _, entry := range respCache.store {
			if !now.Before(entry.expiresAt) {
				respCache.removeEntryLocked(entry, responseCacheRemovalExpiration)
			}
		}
		respCache.mu.Unlock()
	}
}

// expandPreviousResponse 检查请求中是否有 previous_response_id，
// 如果有且缓存命中（且归属于同一 owner），则将历史对话 items 注入到 input 头部。
// 返回处理后的 body 和提取到的 previous_response_id（用于后续缓存链路）。
func expandPreviousResponse(codexBody []byte, owner string) ([]byte, string) {
	prevID := gjson.GetBytes(codexBody, "previous_response_id").String()
	if prevID == "" {
		return codexBody, ""
	}

	currentInput := gjson.GetBytes(codexBody, "input")

	// 客户端已经自带 function_call 等续链项时，跳过注入。
	// 缓存里只会存 function_call 类项（见 cacheCompletedResponse + isCodexToolCallContextType），
	// 再注入会让同一 call_id 出现两次，上游会以 "duplicate call_id" 等 400 拒绝。
	// 仍返回 prevID，让 cacheCompletedResponse 能把这一轮响应链入缓存。
	if currentInput.IsArray() && inputHasToolCallContext(currentInput) {
		log.Printf("input 已自带工具续链项，跳过 previous_response_id=%s 的历史注入", prevID)
		return codexBody, prevID
	}

	cached := getResponseCache(owner, prevID)
	if cached == nil {
		// 缓存未命中（首次请求 / 过期 / 其他实例），无法展开，按原样继续。
		// 若 input 仅含 function_call_output 又拿不到对应的 function_call，
		// 上游通常会返回 "No tool call found for function call output" 400，
		// 这里打日志便于诊断（不阻断，让上游错误透传给客户端）。
		if currentInput.IsArray() && inputHasFunctionCallOutput(currentInput) {
			log.Printf("缓存未命中且 input 含 function_call_output，previous_response_id=%s，上游可能返回 400", prevID)
		}
		return codexBody, prevID
	}

	// 构建新 input: 缓存的历史 items + 当前 input items
	var merged []json.RawMessage
	merged = append(merged, cached...)
	if currentInput.IsArray() {
		currentInput.ForEach(func(_, v gjson.Result) bool {
			merged = append(merged, json.RawMessage(v.Raw))
			return true
		})
	}

	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		log.Printf("展开 previous_response_id 失败: %v", err)
		return codexBody, prevID
	}

	codexBody, _ = sjson.SetRawBytes(codexBody, "input", mergedJSON)
	log.Printf("已展开 previous_response_id=%s，注入 %d 条历史 items", prevID, len(cached))
	return codexBody, prevID
}

// inputHasToolCallContext 判断 input 数组里是否已包含 function_call 类续链项，
// 这类项一旦同时出现在缓存里会造成 call_id 冲突。
func inputHasToolCallContext(input gjson.Result) bool {
	found := false
	input.ForEach(func(_, v gjson.Result) bool {
		if isCodexToolCallContextType(v.Get("type").String()) {
			found = true
			return false
		}
		return true
	})
	return found
}

// inputHasFunctionCallOutput 判断 input 数组里是否含 *_output 项（缺少配对的 function_call 时上游会 400）。
func inputHasFunctionCallOutput(input gjson.Result) bool {
	found := false
	input.ForEach(func(_, v gjson.Result) bool {
		if isCodexToolCallOutputType(v.Get("type").String()) {
			found = true
			return false
		}
		return true
	})
	return found
}

// isCodexToolCallOutputType 判断 item 类型是否属于工具调用输出项（*_call_output），
// 与 isCodexToolCallContextType 的调用项集合一一对应。
func isCodexToolCallOutputType(typ string) bool {
	switch typ {
	case "function_call_output",
		"tool_call_output",
		"local_shell_call_output",
		"shell_call_output",
		"apply_patch_call_output",
		"tool_search_call_output",
		"custom_tool_call_output",
		"mcp_tool_call_output":
		return true
	default:
		return false
	}
}

// cacheCompletedResponse 从 response.completed 事件中提取 response.id 和 response.output，
// 与当前请求的 expanded input 合并后存入 owner 命名空间的缓存。
// 仅在响应包含需要 call_id 续链的 Codex 工具调用时才缓存，避免为普通对话浪费内存。
func cacheCompletedResponse(owner string, expandedInputRaw []byte, completedData []byte) {
	cacheCompletedResponseWithOutputItems(owner, expandedInputRaw, completedData, nil)
}

// cacheCompletedResponseWithOutputItems 在 response.completed.output 未携带工具调用时，
// 使用流式 response.output_item.done 中已收集的 outputItems 作为兜底。
func cacheCompletedResponseWithOutputItems(owner string, expandedInputRaw []byte, completedData []byte, outputItems []json.RawMessage) {
	respID := gjson.GetBytes(completedData, "response.id").String()
	if respID == "" {
		return
	}

	// 仅在响应包含 Codex 工具调用时才缓存（普通对话无需 previous_response_id 展开）。
	// image_generation_call / web_search_call 虽然也是 *_call 结尾，但不属于 call_id 工具续链体系。
	output := gjson.GetBytes(completedData, "response.output")
	if !output.IsArray() && len(outputItems) == 0 {
		return
	}
	completedHasToolCallContext := false
	output.ForEach(func(_, item gjson.Result) bool {
		if isCodexToolCallContextType(item.Get("type").String()) {
			completedHasToolCallContext = true
			return false
		}
		return true
	})
	if !completedHasToolCallContext {
		hasToolCallContext := false
		for _, raw := range outputItems {
			if isCodexToolCallContextType(gjson.GetBytes(raw, "type").String()) {
				hasToolCallContext = true
				break
			}
		}
		if !hasToolCallContext {
			return
		}
	}

	var items []json.RawMessage

	// 添加展开后的请求 input items
	inputItems := gjson.ParseBytes(expandedInputRaw)
	if inputItems.IsArray() {
		inputItems.ForEach(func(_, v gjson.Result) bool {
			if item, ok := replayableCachedInputItem(v); ok {
				items = append(items, item)
			}
			return true
		})
	}

	// 添加响应 output 中真正需要续链的工具上下文；reasoning/message 等
	// 服务端输出 item 带有 rs_/msg_ id，store=false 时回灌会触发 item not found。
	output.ForEach(func(_, v gjson.Result) bool {
		if item, ok := replayableCachedOutputItem(v); ok {
			items = append(items, item)
		}
		return true
	})
	if !completedHasToolCallContext {
		for _, raw := range outputItems {
			if item, ok := replayableCachedOutputItem(gjson.ParseBytes(raw)); ok {
				items = append(items, item)
			}
		}
	}

	if len(items) > 0 {
		setResponseCache(owner, respID, items)
	}
}

func replayableCachedInputItem(item gjson.Result) (json.RawMessage, bool) {
	if item.Get("type").String() == "reasoning" && item.Get("encrypted_content").Exists() {
		return nil, false
	}
	return stripResponseItemID(json.RawMessage(item.Raw))
}

func replayableCachedOutputItem(item gjson.Result) (json.RawMessage, bool) {
	if !isCodexToolCallContextType(item.Get("type").String()) {
		return nil, false
	}
	return stripResponseItemID(json.RawMessage(item.Raw))
}

func stripResponseItemID(raw json.RawMessage) (json.RawMessage, bool) {
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil || item == nil {
		return raw, true
	}
	if _, exists := item["id"]; !exists {
		return raw, true
	}
	delete(item, "id")
	stripped, err := json.Marshal(item)
	if err != nil {
		return nil, false
	}
	return stripped, true
}

func isCodexToolCallContextType(typ string) bool {
	switch typ {
	case "function_call",
		"tool_call",
		"local_shell_call",
		"shell_call",
		"apply_patch_call",
		"tool_search_call",
		"custom_tool_call",
		"mcp_tool_call":
		return true
	default:
		return false
	}
}
