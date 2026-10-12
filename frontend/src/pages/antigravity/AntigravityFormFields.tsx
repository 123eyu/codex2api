import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Check } from "lucide-react";
import type { ProxyRow } from "../../api";
import type { AccountGroup } from "../../types";
import AccountGroupMultiSelect from "../../components/AccountGroupMultiSelect";
import { ProxyField } from "../../components/ProxyField";
import { ANTIGRAVITY_DEFAULT_MODELS } from "../../lib/antigravityModels";
import { parseModelList } from "../../lib/antigravityQuota";
import { cn } from "@/lib/utils";

export const TEXTAREA_CLASS =
  "w-full resize-y rounded-md border border-input bg-background px-3 py-2 font-mono text-xs text-foreground outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/30";

export function FieldLabel({ children }: { children: ReactNode }) {
  return <span className="text-xs font-semibold text-muted-foreground">{children}</span>;
}

export function FieldHint({ children, tone = "muted" }: { children: ReactNode; tone?: "muted" | "warning" }) {
  return (
    <p
      className={cn(
        "text-[11px] leading-relaxed",
        tone === "warning" ? "text-amber-700 dark:text-amber-300" : "text-muted-foreground",
      )}
    >
      {children}
    </p>
  );
}

/** 弹窗内的分块:小标题 + 卡片容器,用来把长表单切成几段。 */
export function FormSection({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <section className="space-y-3 rounded-xl border border-border bg-card/60 p-3.5">
      <div>
        <h3 className="text-[12px] font-semibold text-foreground">{title}</h3>
        {description ? <p className="mt-0.5 text-[11px] text-muted-foreground">{description}</p> : null}
      </div>
      {children}
    </section>
  );
}

/** 两段式切换(凭据类型等),样式与渠道页工具栏的分段控件一致。 */
export function SegmentedChoice<Value extends string>({
  value,
  options,
  onChange,
  className,
}: {
  value: Value;
  options: Array<{ value: Value; label: string; icon?: ReactNode }>;
  onChange: (value: Value) => void;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "grid rounded-lg border border-border bg-muted/40 p-0.5",
        className,
      )}
      style={{ gridTemplateColumns: `repeat(${options.length}, minmax(0, 1fr))` }}
      role="tablist"
    >
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          role="tab"
          aria-selected={value === option.value}
          onClick={() => onChange(option.value)}
          className={cn(
            "inline-flex min-w-0 items-center justify-center gap-1.5 rounded-md px-2.5 py-1.5 text-[13px] font-semibold transition-colors",
            value === option.value
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {option.icon}
          <span className="truncate">{option.label}</span>
        </button>
      ))}
    </div>
  );
}

export function AccountMetadataFields({
  proxyUrl,
  onProxyUrlChange,
  proxies = [],
  groupIds,
  onGroupIdsChange,
  groups,
  onCreateGroup,
}: {
  proxyUrl: string;
  onProxyUrlChange: (value: string) => void;
  proxies?: ProxyRow[];
  groupIds: number[];
  onGroupIdsChange: (value: number[]) => void;
  groups: AccountGroup[];
  onCreateGroup: (name: string) => Promise<number | null>;
}) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      {/* 代理字段与其他三个渠道同构:手填 + 测试 + 从代理池选择(含关联提示)。 */}
      <ProxyField
        className="space-y-1.5"
        value={proxyUrl}
        onChange={onProxyUrlChange}
        proxies={proxies}
        label={t("antigravity.proxyUrl")}
        placeholder={t("antigravity.proxyUrlPlaceholder")}
      />
      <div className="space-y-1.5">
        <FieldLabel>{t("accounts.groupsLabel")}</FieldLabel>
        <AccountGroupMultiSelect
          groups={groups}
          value={groupIds}
          onChange={onGroupIdsChange}
          placeholder={t("accounts.groupsPlaceholder")}
          emptyLabel={t("accounts.groupsNone")}
          emptyHint={t("accounts.groupsSelectHint")}
          selectedLabel={t("accounts.groupsSelected", { count: groupIds.length })}
          onCreateGroup={onCreateGroup}
          createLabel={t("accounts.groupCreate")}
          createPlaceholder={t("accounts.groupNamePlaceholder")}
          creatingLabel={t("accounts.groupCreating")}
          createEmptyHint={t("accounts.groupCreateInlineEmptyHint")}
        />
      </div>
    </div>
  );
}

/** 默认模型快捷芯片:点击在模型列表里加入/移除该模型。 */
export function DefaultModelChips({
  value,
  onChange,
  disabled = false,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}) {
  const current = parseModelList(value);
  const selected = new Set(current.map((model) => model.toLowerCase()));
  return (
    <div className="flex flex-wrap gap-1.5">
      {ANTIGRAVITY_DEFAULT_MODELS.map((model) => {
        const active = selected.has(model.toLowerCase());
        return (
          <button
            key={model}
            type="button"
            disabled={disabled}
            aria-pressed={active}
            onClick={() =>
              onChange(
                (active
                  ? current.filter((item) => item.toLowerCase() !== model.toLowerCase())
                  : [...current, model]
                ).join("\n"),
              )
            }
            className={cn(
              "inline-flex items-center gap-1 rounded-md border px-2 py-1 font-mono text-[11px] transition-colors disabled:opacity-50",
              active
                ? "border-primary/40 bg-primary/10 text-primary"
                : "border-border bg-background text-muted-foreground hover:border-primary/30 hover:text-foreground",
            )}
          >
            {active ? <Check className="size-3" aria-hidden /> : null}
            {model}
          </button>
        );
      })}
    </div>
  );
}

/** API Key 账号的模型列表 + 模型映射字段组(导入与编辑共用)。 */
export function APIKeyModelFields({
  models,
  modelMapping,
  onModelsChange,
  onModelMappingChange,
}: {
  models: string;
  modelMapping: string;
  onModelsChange: (value: string) => void;
  onModelMappingChange: (value: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="space-y-3">
      <label className="block space-y-1.5">
        <FieldLabel>
          {t("antigravity.models")}
          <span className="ml-1.5 font-normal text-muted-foreground/80">
            {t("antigravity.modelsCount", { count: parseModelList(models).length })}
          </span>
        </FieldLabel>
        <textarea
          value={models}
          onChange={(event) => onModelsChange(event.target.value)}
          rows={4}
          spellCheck={false}
          placeholder={t("antigravity.modelsPlaceholder")}
          className={TEXTAREA_CLASS}
        />
      </label>
      <DefaultModelChips value={models} onChange={onModelsChange} />
      <label className="block space-y-1.5">
        <FieldLabel>{t("antigravity.modelMapping")}</FieldLabel>
        <textarea
          value={modelMapping}
          onChange={(event) => onModelMappingChange(event.target.value)}
          rows={3}
          spellCheck={false}
          placeholder={t("antigravity.modelMappingPlaceholder")}
          className={TEXTAREA_CLASS}
        />
        <FieldHint>{t("antigravity.modelMappingHint")}</FieldHint>
      </label>
      <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
        {t("antigravity.interactionsExperimental")}
      </div>
    </div>
  );
}
