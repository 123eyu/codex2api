package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPromptRiskAdaptiveTrustLifecycleAndAutomaticSuspension(t *testing.T) {
	db := newPromptPolicySQLiteTestDB(t)
	ctx := context.Background()
	if err := db.InsertPromptFilterLog(ctx, &PromptFilterLogInput{
		Source: "local_filter", Action: "allow", ReviewModel: "review-model", ReviewFlagged: false,
		NewAPIPolicyStatus: "verified", NewAPIPlatform: "gateway-a", NewAPIUserID: "trusted-user",
	}); err != nil {
		t.Fatalf("InsertPromptFilterLog(clean): %v", err)
	}
	subjectKey := PromptRiskNewAPIUserSubjectKey("gateway-a", "trusted-user")
	policy, err := db.UpsertPromptRiskTrustPolicy(ctx, PromptRiskTrustPolicyInput{
		SubjectType: PromptRiskSubjectNewAPIUser, SubjectKey: subjectKey, Reason: "低风险付费用户首字优化",
		RiskThreshold: 35, ValidUntil: time.Now().UTC().Add(24 * time.Hour),
	})
	if err != nil || policy.Status != PromptRiskTrustStatusActive {
		t.Fatalf("UpsertPromptRiskTrustPolicy policy=%#v err=%v", policy, err)
	}
	if err := db.RecordPromptRiskTrustBypass(ctx, policy.ID, policy.SubjectType, policy.SubjectKey, "request-hash"); err != nil {
		t.Fatalf("RecordPromptRiskTrustBypass: %v", err)
	}
	policy, err = db.GetPromptRiskTrustPolicy(ctx, policy.SubjectType, policy.SubjectKey)
	if err != nil || policy.BypassCount != 1 || policy.LastBypassAt == nil {
		t.Fatalf("bypass audit policy=%#v err=%v", policy, err)
	}
	if err := db.InsertPromptFilterLog(ctx, &PromptFilterLogInput{
		Source: "local_filter", Action: "block", Score: 90, AuditScore: 90, StrikeEligible: true,
		MatchedPatterns: `[{"name":"terminal","weight":90}]`, NewAPIPolicyStatus: "verified",
		NewAPIPlatform: "gateway-a", NewAPIUserID: "trusted-user",
	}); err != nil {
		t.Fatalf("InsertPromptFilterLog(block): %v", err)
	}
	active, err := db.ReconcilePromptRiskTrustPolicies(ctx)
	if err != nil {
		t.Fatalf("ReconcilePromptRiskTrustPolicies: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("high-risk policy remained active: %#v", active)
	}
	policy, err = db.GetPromptRiskTrustPolicy(ctx, policy.SubjectType, policy.SubjectKey)
	if err != nil || policy.Status != PromptRiskTrustStatusSuspended || policy.LastRiskScore < 35 {
		t.Fatalf("automatic suspension policy=%#v err=%v", policy, err)
	}
	if err := db.RecordPromptRiskTrustBypass(ctx, policy.ID, policy.SubjectType, policy.SubjectKey, "late-request-hash"); err != nil {
		t.Fatalf("RecordPromptRiskTrustBypass(suspended): %v", err)
	}
	policy, err = db.GetPromptRiskTrustPolicy(ctx, policy.SubjectType, policy.SubjectKey)
	if err != nil || policy.BypassCount != 1 {
		t.Fatalf("suspended policy accepted a late bypass: policy=%#v err=%v", policy, err)
	}
	events, err := db.ListPromptRiskTrustEvents(ctx, policy.SubjectType, policy.SubjectKey, 20)
	if err != nil || len(events) < 3 || events[0].EventType != PromptRiskTrustEventAutoSuspended {
		t.Fatalf("trust audit events=%#v err=%v", events, err)
	}
}

func TestPromptRiskAdaptiveTrustRejectsNonPersonAndPermanentGrant(t *testing.T) {
	db := newPromptPolicySQLiteTestDB(t)
	ctx := context.Background()
	if _, err := db.UpsertPromptRiskTrustPolicy(ctx, PromptRiskTrustPolicyInput{
		SubjectType: PromptRiskSubjectSession, SubjectKey: "session", Reason: "invalid", RiskThreshold: 35,
		ValidUntil: time.Now().UTC().Add(time.Hour),
	}); err == nil {
		t.Fatal("non-person trust grant unexpectedly succeeded")
	}
	if _, err := db.UpsertPromptRiskTrustPolicy(ctx, PromptRiskTrustPolicyInput{
		SubjectType: PromptRiskSubjectNewAPIUser, SubjectKey: "person", Reason: "too long", RiskThreshold: 35,
		ValidUntil: time.Now().UTC().Add(31 * 24 * time.Hour),
	}); err == nil {
		t.Fatal("permanent-like trust grant unexpectedly succeeded")
	}
}

func TestPromptRiskAdaptiveTrustAutomaticallyPromotesStableLowRiskPerson(t *testing.T) {
	db := newPromptPolicySQLiteTestDB(t)
	ctx := context.Background()
	if err := db.ensurePromptRiskEventsTable(ctx); err != nil {
		t.Fatalf("ensurePromptRiskEventsTable: %v", err)
	}
	subjectKey := PromptRiskNewAPIUserSubjectKey("gateway-a", "stable-user")
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		createdAt := now.Add(-time.Duration(25-i) * time.Hour)
		if _, err := db.conn.ExecContext(ctx, `INSERT INTO prompt_risk_events (
			created_at, source_type, source_id, request_correlation_id, subject_type, subject_key, subject_display,
			platform, is_person, identity_confidence, event_kind, request_risk_score, evidence_confidence
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,TRUE,100,'review_cleared',0,95)`, createdAt, promptRiskSourceLog,
			fmt.Sprintf("clean-%d", i), fmt.Sprintf("request-%d", i), PromptRiskSubjectNewAPIUser, subjectKey, "stable-user", "gateway-a"); err != nil {
			t.Fatalf("insert clean risk event %d: %v", i, err)
		}
	}
	policies, err := db.ReconcileAdaptivePromptRiskTrustPolicies(ctx, PromptRiskTrustAdaptiveOptions{
		MinCleanReviews: 10, MinObservationHours: 24, TrustDurationHours: 168,
		ReactivationCleanReviews: 5, ReactivationCooldownHours: 24, RiskThreshold: 35,
	})
	if err != nil {
		t.Fatalf("ReconcileAdaptivePromptRiskTrustPolicies: %v", err)
	}
	if len(policies) != 1 || policies[0].SubjectKey != subjectKey || policies[0].Source != PromptRiskTrustSourceAutomatic || policies[0].LastModelReviewAt == nil {
		t.Fatalf("stable low-risk person was not automatically promoted: %#v", policies)
	}
	if err := db.RecordPromptRiskTrustModelReview(ctx, policies[0].ID, policies[0].SubjectType, policies[0].SubjectKey, "sample-hash"); err != nil {
		t.Fatalf("RecordPromptRiskTrustModelReview: %v", err)
	}
	policy, err := db.GetPromptRiskTrustPolicy(ctx, PromptRiskSubjectNewAPIUser, subjectKey)
	if err != nil || policy.ModelReviewCount != 1 || policy.LastModelReviewAt == nil {
		t.Fatalf("model review audit was not persisted: policy=%#v err=%v", policy, err)
	}
	events, err := db.ListPromptRiskTrustEvents(ctx, PromptRiskSubjectNewAPIUser, subjectKey, 10)
	if err != nil || len(events) < 2 || events[0].EventType != PromptRiskTrustEventModelReviewed {
		t.Fatalf("automatic trust events=%#v err=%v", events, err)
	}
}

func TestPromptRiskAdaptiveTrustMigratesLegacySQLitePolicyTable(t *testing.T) {
	db := newPromptPolicySQLiteTestDB(t)
	ctx := context.Background()
	if _, err := db.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS prompt_risk_trust_policies (
		id INTEGER PRIMARY KEY AUTOINCREMENT, subject_type TEXT NOT NULL, subject_key TEXT NOT NULL UNIQUE,
		status TEXT NOT NULL DEFAULT 'active', reason TEXT NOT NULL DEFAULT '', risk_threshold INTEGER NOT NULL DEFAULT 35,
		valid_until TIMESTAMP NOT NULL, last_evaluated_at TIMESTAMP NULL, last_risk_score INTEGER NOT NULL DEFAULT 0,
		last_risk_level TEXT NOT NULL DEFAULT 'low', bypass_count INTEGER NOT NULL DEFAULT 0, last_bypass_at TIMESTAMP NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create legacy trust table: %v", err)
	}
	if err := db.ensurePromptRiskTrustTables(ctx); err != nil {
		t.Fatalf("ensurePromptRiskTrustTables: %v", err)
	}
	columns, err := db.sqliteTableColumns(ctx, "prompt_risk_trust_policies")
	if err != nil {
		t.Fatalf("sqliteTableColumns: %v", err)
	}
	for _, name := range []string{"source", "model_review_count", "last_model_review_at"} {
		if _, ok := columns[name]; !ok {
			t.Fatalf("legacy trust table missing migrated column %q", name)
		}
	}
}

func TestPromptRiskAdaptiveTrustPostgresMigrationDDL(t *testing.T) {
	promptPolicyDDLDriverOnce.Do(func() { sql.Register("prompt-policy-ddl-capture", promptPolicyDDLDriver{}) })
	promptPolicyDDLQueryMu.Lock()
	promptPolicyDDLQueries = nil
	promptPolicyDDLQueryMu.Unlock()
	conn, err := sql.Open("prompt-policy-ddl-capture", "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()
	db := &DB{conn: conn, driver: "postgres"}
	if err := db.ensurePromptRiskTrustTables(context.Background()); err != nil {
		t.Fatalf("ensurePromptRiskTrustTables: %v", err)
	}
	promptPolicyDDLQueryMu.Lock()
	joined := strings.Join(promptPolicyDDLQueries, "\n")
	promptPolicyDDLQueryMu.Unlock()
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS prompt_risk_trust_policies",
		"source VARCHAR(24) NOT NULL DEFAULT 'manual'",
		"model_review_count BIGINT NOT NULL DEFAULT 0",
		"last_model_review_at TIMESTAMP NULL",
		"ALTER TABLE prompt_risk_trust_policies ADD COLUMN IF NOT EXISTS source",
	} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("postgres adaptive trust migration missing %q: %s", fragment, joined)
		}
	}
}
