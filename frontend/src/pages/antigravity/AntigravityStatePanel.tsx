import { useState } from "react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  CheckCircle2,
  ExternalLink,
  FileJson,
  FlaskConical,
  KeyRound,
  Loader2,
  RotateCw,
  ShieldCheck,
  ShieldQuestion,
  ShieldX,
  TriangleAlert,
} from "lucide-react";
import type {
  AccountRow,
  AntigravityAccountState,
  AntigravityPermissionsSnapshot,
} from "../../types";
import ChannelLogo from "../../components/ChannelLogo";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import {
  firstString,
  firstWarningURL,
  identityAvatarURL,
  identityProjectID,
  permissionAllowed,
  subscriptionTier,
  tierLabel,
  toRecord,
} from "../../lib/antigravityQuota";
import { formatRelativeTime } from "../../utils/time";
import { AntigravityModelQuotaList } from "./AntigravityQuotaCells";

export function AccountAvatar({ account, size = 36 }: { account: AccountRow; size?: number }) {
  const [failed, setFailed] = useState(false);
  const avatar = identityAvatarURL(account);
  if (avatar && !failed) {
    return (
      <img
        src={avatar}
        alt=""
        width={size}
        height={size}
        referrerPolicy="no-referrer"
        className="shrink-0 rounded-full border border-border bg-background object-cover"
        style={{ width: size, height: size }}
        onError={() => setFailed(true)}
      />
    );
  }
  return (
    <span
      className="inline-flex shrink-0 items-center justify-center rounded-full border border-border bg-background"
      style={{ width: size, height: size }}
    >
      <ChannelLogo channel="antigravity" size={Math.round(size * 0.58)} />
    </span>
  );
}

// 权限列里的操作入口:同步 warning 会带出 Google 的一次性操作链接
// (TOS 申诉表单 / 账号验证),直接渲染成可点击入口,免得去详情页复制长 URL。
export function SyncWarningAction({ warning }: { warning?: string }) {
  const { t } = useTranslation();
  const url = firstWarningURL(warning);
  if (!url) return null;
  const label = /appeal/i.test(warning ?? "")
    ? t("antigravity.submitAppeal")
    : t("antigravity.verifyAccount");
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      onClick={(event) => event.stopPropagation()}
      className="mt-1 inline-flex max-w-[180px] items-center gap-1 text-[11px] font-medium text-primary hover:underline"
    >
      <ExternalLink className="size-3 shrink-0" aria-hidden />
      <span className="truncate">{label}</span>
    </a>
  );
}

// 详情 warning 横幅:URL 转成可点击链接,展示文本截短避免长链接刷屏。
export function LinkifiedWarning({ text }: { text: string }) {
  const parts = text.split(/(https?:\/\/[^\s;]+)/g);
  return (
    <span className="break-words">
      {parts.map((part, index) => {
        if (!/^https?:\/\//.test(part)) {
          return part;
        }
        const display = part.length > 64 ? part.slice(0, 61) + "…" : part;
        return (
          <a
            key={index}
            href={part}
            target="_blank"
            rel="noopener noreferrer"
            className="font-medium underline underline-offset-2"
          >
            {display}
          </a>
        );
      })}
    </span>
  );
}

export function PermissionBadge({ permissions }: { permissions?: AntigravityPermissionsSnapshot }) {
  const { t } = useTranslation();
  const allowed = permissionAllowed(permissions);
  if (allowed == null) {
    return (
      <Badge variant="outline" className="gap-1 text-muted-foreground">
        <ShieldQuestion className="size-3" />
        {t("antigravity.permissionUnknown")}
      </Badge>
    );
  }
  if (allowed) {
    return (
      <Badge className="gap-1 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300">
        <ShieldCheck className="size-3" />
        {t("antigravity.permissionAllowed")}
      </Badge>
    );
  }
  return (
    <Badge variant="destructive" className="gap-1">
      <ShieldX className="size-3" />
      {t("antigravity.permissionBlocked")}
    </Badge>
  );
}

// AntigravityAuthKindChip 凭据形态小徽章(与 Grok 页同款配色:OAuth 紫 / API Key 天蓝)。
export function AntigravityAuthKindChip({ account }: { account: AccountRow }) {
  const { t } = useTranslation();
  const isAPIKey = account.antigravity_auth_kind === "api_key";
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-0.5 whitespace-nowrap rounded-md px-1.5 py-0.5 text-[10px] font-medium ring-1 ring-inset",
        isAPIKey
          ? "bg-sky-500/10 text-sky-700 ring-sky-600/20 dark:bg-sky-500/20 dark:text-sky-300 dark:ring-sky-400/20"
          : "bg-violet-500/10 text-violet-700 ring-violet-600/20 dark:bg-violet-500/20 dark:text-violet-300 dark:ring-violet-400/20",
      )}
    >
      {isAPIKey ? <KeyRound className="size-2.5" /> : <FileJson className="size-2.5" />}
      {isAPIKey ? t("antigravity.authKindApiKey") : t("antigravity.authKindOAuth")}
    </span>
  );
}

function PanelSection({
  title,
  action,
  children,
}: {
  title: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="space-y-2.5">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">{title}</h3>
        {action}
      </div>
      {children}
    </section>
  );
}

function FactRow({ label, children, mono = false }: { label: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-3 text-[12px]">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <span className={cn("min-w-0 break-all text-right text-foreground", mono && "font-mono text-[11px]")}>
        {children}
      </span>
    </div>
  );
}

/**
 * 详情抽屉 providerSlot:同步告警、Google 控制面状态(可同步/探测)、Code Assist
 * 权限与套餐、逐模型额度。只读展示持久化观测,不主动打上游。
 */
export function AntigravityProviderPanel({
  account,
  state,
  loading,
  error,
  action,
  onSync,
  onProbe,
}: {
  account: AccountRow;
  state: AntigravityAccountState | null;
  loading: boolean;
  error: string | null;
  action: "sync" | "probe" | null;
  onSync: () => void;
  onProbe: () => void;
}) {
  const { t } = useTranslation();
  const isAPIKey = account.antigravity_auth_kind === "api_key";
  const latest = state?.capabilities?.[0];
  const permissions = account.antigravity_permissions;
  const allowed = permissionAllowed(permissions);
  const permissionRecord = toRecord(permissions);
  const currentTier = tierLabel(permissionRecord.current_tier ?? permissionRecord.currentTier);
  const paidTier = tierLabel(permissionRecord.paid_tier ?? permissionRecord.paidTier);
  const allowedTiersRaw = permissionRecord.allowed_tiers ?? permissionRecord.allowedTiers;
  const ineligibleTiersRaw = permissionRecord.ineligible_tiers ?? permissionRecord.ineligibleTiers;
  const allowedTiers = Array.isArray(allowedTiersRaw) ? allowedTiersRaw.map(tierLabel).filter(Boolean) : [];
  const ineligibleTiers = Array.isArray(ineligibleTiersRaw)
    ? ineligibleTiersRaw.map(tierLabel).filter(Boolean)
    : [];
  const reason = firstString(permissionRecord, ["reason"]);
  const lastSynced = state?.last_synced_at || state?.last_sync_attempt_at;

  const stateFacts: Array<[string, string]> = state
    ? [
        [t("antigravity.stateCredential"), state.credential_kind === "api_key" ? t("antigravity.authKindApiKey") : t("antigravity.authKindOAuth")],
        [t("antigravity.stateIdentity"), state.identity.status],
        [t("antigravity.stateCatalog"), `${state.catalog.models.length} · ${state.catalog.source}`],
        [t("antigravity.stateVerification"), state.catalog.verified ? t("antigravity.verified") : t("antigravity.unverified")],
      ]
    : [];

  return (
    <div className="space-y-5">
      {account.antigravity_sync_warning ? (
        <div className="flex items-start gap-2 rounded-xl border border-amber-300/70 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-200">
          <TriangleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          <LinkifiedWarning text={account.antigravity_sync_warning} />
        </div>
      ) : null}

      <PanelSection
        title={t("antigravity.stateTitle")}
        action={
          <div className="flex items-center gap-1">
            <Button variant="ghost" size="xs" className="h-7 text-[11px]" disabled={loading || action !== null} onClick={onSync}>
              <RotateCw className={cn("size-3", action === "sync" && "animate-spin")} />
              {action === "sync" ? t("antigravity.stateSyncing") : t("antigravity.stateSync")}
            </Button>
            <Button variant="ghost" size="xs" className="h-7 text-[11px]" disabled={loading || action !== null} onClick={onProbe}>
              <FlaskConical className={cn("size-3", action === "probe" && "animate-pulse")} />
              {action === "probe" ? t("antigravity.capabilityProbing") : t("antigravity.capabilityProbe")}
            </Button>
          </div>
        }
      >
        <div className="space-y-3 rounded-xl border border-border bg-card p-3">
          <p className="text-[11px] leading-relaxed text-muted-foreground">{t("antigravity.stateDescription")}</p>
          {loading ? (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" />
              {t("antigravity.stateLoading")}
            </div>
          ) : null}
          {error ? (
            <div className="rounded-lg bg-destructive/10 px-3 py-2 text-xs text-destructive">{error}</div>
          ) : null}
          {state ? (
            <>
              <div className="grid grid-cols-2 gap-2">
                {stateFacts.map(([label, value]) => (
                  <div key={label} className="min-w-0 rounded-lg bg-muted/35 px-2.5 py-2">
                    <div className="text-[10px] text-muted-foreground">{label}</div>
                    <div className="mt-0.5 truncate text-xs font-semibold" title={value}>
                      {value}
                    </div>
                  </div>
                ))}
              </div>
              <div className="rounded-lg border border-border/70 px-3 py-2 text-xs">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-semibold">{t("antigravity.capabilityTitle")}</span>
                  <Badge variant={latest?.verified ? "default" : "outline"}>
                    {latest?.verified ? t("antigravity.verified") : t("antigravity.notProbed")}
                  </Badge>
                </div>
                <div className="mt-1 break-all text-[11px] text-muted-foreground">
                  {latest
                    ? `${latest.protocol} · ${latest.model_id} · ${latest.status}${latest.http_status ? ` · HTTP ${latest.http_status}` : ""}`
                    : t("antigravity.capabilityEmpty")}
                </div>
              </div>
              {lastSynced ? (
                <div className="text-[10px] text-muted-foreground">
                  {t("antigravity.lastSyncedAt", { time: formatRelativeTime(lastSynced, { variant: "compact" }) })}
                </div>
              ) : null}
              {state.warnings.length > 0 ? (
                <div className="space-y-1 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-[11px] text-amber-700 dark:text-amber-300">
                  {state.warnings.map((warning) => (
                    <div key={warning}>{warning}</div>
                  ))}
                </div>
              ) : null}
            </>
          ) : null}
        </div>
      </PanelSection>

      {!isAPIKey ? (
        <PanelSection title={t("antigravity.permissionsTitle")} action={<PermissionBadge permissions={permissions} />}>
          <div className="space-y-2 rounded-xl border border-sky-200/70 bg-sky-50/40 p-3 dark:border-sky-900/50 dark:bg-sky-950/15">
            {allowed === false && reason ? (
              <div className="flex items-start gap-1.5 rounded-lg bg-red-500/10 px-2.5 py-2 text-[12px] text-red-700 dark:text-red-300">
                <ShieldX className="mt-0.5 size-3.5 shrink-0" aria-hidden />
                <span className="break-words">{reason}</span>
              </div>
            ) : null}
            <FactRow label={t("antigravity.subscriptionTier")}>
              <span className="font-semibold">{subscriptionTier(account) || currentTier || "-"}</span>
            </FactRow>
            <FactRow label={t("antigravity.projectId")} mono>
              {identityProjectID(account) || t("antigravity.projectPending")}
            </FactRow>
            {currentTier ? <FactRow label={t("antigravity.currentTier")}>{currentTier}</FactRow> : null}
            {paidTier ? <FactRow label={t("antigravity.paidTier")}>{paidTier}</FactRow> : null}
            {allowedTiers.length > 0 ? (
              <div className="space-y-1 pt-1">
                <div className="text-[11px] text-muted-foreground">{t("antigravity.allowedTiers")}</div>
                <div className="flex flex-wrap gap-1">
                  {allowedTiers.map((tier, index) => (
                    <Badge key={`${tier}-${index}`} variant="outline" className="gap-1 text-[10px]">
                      <CheckCircle2 className="size-2.5 text-emerald-500" />
                      {tier}
                    </Badge>
                  ))}
                </div>
              </div>
            ) : null}
            {ineligibleTiers.length > 0 ? (
              <div className="space-y-1 pt-1">
                <div className="text-[11px] text-muted-foreground">{t("antigravity.ineligibleTiers")}</div>
                <div className="flex flex-wrap gap-1">
                  {ineligibleTiers.map((tier, index) => (
                    <Badge key={`${tier}-${index}`} variant="outline" className="text-[10px] text-muted-foreground">
                      {tier}
                    </Badge>
                  ))}
                </div>
              </div>
            ) : null}
          </div>
        </PanelSection>
      ) : null}

      {!isAPIKey ? (
        <PanelSection title={t("antigravity.modelQuotaTitle")}>
          <AntigravityModelQuotaList account={account} />
        </PanelSection>
      ) : null}
    </div>
  );
}
