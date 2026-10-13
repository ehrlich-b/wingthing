// A deterministic, provider-free contract for dormant paid UI. No fetch, keys,
// process launch, payment SDK, device grants or production account writes.
export const FIXTURE_VERSION = 1;
export const AS_OF = '2026-10-12T21:00:00Z';
export const TERMS = Object.freeze({ monthlyCents: 2900, includedSeconds: 72000, storageGB: 10,
    outboundGB: 100, outboundCents: 8, shutdownSeconds: 120, idleMinutes: 15, warningMinutes: 2 });
export const SIZES = Object.freeze({ standard: { name: 'Standard', cpu: 2, ram: 4, multiplier: 1, cents: 35 },
    large: { name: 'Large', cpu: 2, ram: 8, multiplier: 2, cents: 70 } });
export const REPOSITORIES = ['sample/project', 'sample/docs'];
export const DOMAINS = ['github.com', 'registry.npmjs.org', 'pypi.org', 'proxy.golang.org', 'api.anthropic.com', 'api.openai.com'];
const clone = value => structuredClone(value);
function demand(condition, message) { if (!condition) throw new Error(message); }
function number(value, min, max, label) {
    demand(typeof value === 'number' && Number.isFinite(value) && value >= min && value <= max, `Invalid ${label}`);
}
function id(value) { demand(typeof value === 'string' && /^[a-zA-Z0-9_-]{1,80}$/.test(value), 'Invalid fixture ID'); }
function text(value, max = 500) { demand(typeof value === 'string' && value.length > 0 && value.length <= max, 'Invalid text'); }
const allowedFields = {
    intent: ['moment', 'task', 'route'], dismiss: ['moment'], checkout: ['requestId'],
    'checkout-return': [], payment: ['eventId', 'checkoutId', 'outcome'], 'payment-check': [],
    'owner-grant': ['accountId', 'reason'], 'owner-revoke': [], cancel: [], expire: [], 'advance-time': ['at'],
    draft: ['task', 'workspace', 'size', 'hours', 'capCents', 'overage'],
    'phone-request': ['templateId', 'task'], 'template-approve': [],
    'repo-connect': ['repository', 'branch'], 'git-clone': [], 'git-push': ['branch'],
    'vault-toggle': ['name', 'enabled'], 'model-select': ['agent', 'mode', 'consent'],
    'network-toggle': ['domain', 'enabled'], 'tool-add': ['tool'],
    launch: ['requestId'], 'launch-step': [], 'launch-fail': ['phase'], 'launch-retry': [],
    attach: ['wingId', 'sessionId'], 'cloud-pair': ['wingId'],
    meter: ['eventId', 'seconds', 'verified'], stop: [], 'stop-confirm': ['eventId'],
    activity: ['active', 'minutes'], 'idle-check': [], keepalive: [],
    resume: ['requestId', 'coldConsent'], 'preview-share': ['recipient'],
    export: [], 'delete-review': [], delete: ['volumeId'],
    fault: ['kind'], 'recover-cold': ['consent'], 'account-cap': ['capCents'],
    'invoice-finalize': ['eventId', 'invoiceId'], 'billing-fail': ['invoiceId'], 'billing-recover': [],
    notification: ['name', 'enabled'], support: [], reconcile: ['eventId', 'usageEventId', 'decision'],
    'team-check': ['tenant', 'role', 'resource', 'revoked'],
};
export const operations = Object.keys(allowedFields);
export function createPaidState(scenario = 'free') {
    demand(['free', 'active', 'operator', 'phone', 'unapproved-phone'].includes(scenario), 'Unknown fixture scenario');
    const s = {
        fixture: true, version: FIXTURE_VERSION, sourceAt: AS_OF, asOf: AS_OF, principal: scenario === 'operator' ? 'operator' : scenario.includes('phone') ? 'phone' : 'owner',
        accountId: 'fixture-account-1', displayName: 'Fixture owner', pro: scenario !== 'free', entitlementVersion: scenario === 'free' ? 0 : 1,
        entitlementSource: scenario === 'free' ? 'none' : 'fixture-payment', paidThrough: '2026-11-12T21:00:00Z', cancellation: false,
        exportUntil: null, checkout: null, paymentEvents: [], invoiceEvents: [], meterEvents: [], pendingIntervals: {}, stopEvents: {}, settledRuns: {},
        intent: null, offersSeen: [], accountCapCents: 0, consumedSeconds: 0, consumedCents: 0,
        invoice: { id: 'fixture-invoice-1', status: 'draft', cents: 2900, lines: [{ label: 'Pro subscription (proposal)', cents: 2900 }], events: [] },
        billingBlocked: false, meterFresh: true,
        draft: { task: 'Review pairing implementation', workspace: 'project', size: 'standard', hours: 2, capCents: 0, overage: false,
            repository: 'sample/project', branch: 'cloud/review', repoAuthorized: true, cloned: false, gitStatus: 'not cloned',
            agent: 'claude', mode: 'byo', modelConsent: true, modelAvailable: true, network: [...DOMAINS],
            vault: { DEPLOY_TOKEN: false, DATABASE_URL: false }, image: 'fixture-linux-2026.10', tools: ['wt', 'Claude', 'Codex', 'Git', 'Node LTS', 'Python', 'Go'] },
        cloud: null, launchRecords: {}, resumeRecords: {}, reservation: null, nextRun: 0, nextCloud: 1,
        template: { id: 'fixture-template-1', approved: scenario !== 'unapproved-phone', size: 'standard', hours: 2, capCents: 0, coordinatorWingId: 'fixture-coordinator-wing', coordinatorSessionId: 'fixture-coordinator-session' },
        phoneRequest: null, phonePairedWings: ['fixture-local-wing', 'fixture-coordinator-wing'], localSessions: ['fixture-local-session'], localDeviceGrants: ['fixture-local-wing'],
        notifications: { budget: true, billing: true, workspace: true, attention: false }, report: null, deletionReview: false,
        fault: null, audit: [], teamDecision: null, message: 'Dormant fixture. All amounts are proposed; no providers are connected.',
    };
    return s;
}
export function quote(s) {
    const d = s.draft, size = SIZES[d.size];
    const seconds = Math.ceil((d.hours * 3600 + TERMS.shutdownSeconds) * size.multiplier);
    const available = Math.max(0, TERMS.includedSeconds - s.consumedSeconds - (s.reservation?.seconds || 0));
    const extraSeconds = Math.max(0, seconds - available);
    const cents = Math.ceil(extraSeconds * SIZES.standard.cents / 3600);
    return { seconds, available, extraSeconds, cents, bufferSeconds: TERMS.shutdownSeconds * size.multiplier,
        billableHours: d.hours, rateCents: size.cents, permitted: extraSeconds === 0 || (d.overage && cents <= d.capCents && s.consumedCents + cents <= s.accountCapCents) };
}
export function shouldOffer(s, { moment, route = 'relay' }, enabled) {
    return enabled === true && !s.pro && ['reach', 'publish', 'cloud', 'share'].includes(moment) &&
        !(moment === 'reach' && ['lan', 'tailnet'].includes(route)) && !s.offersSeen.includes(moment);
}
function audit(s, op, details = {}) { s.audit.push({ id: `fixture-audit-${s.audit.length + 1}`, at: s.asOf, actor: s.principal, accountId: s.accountId, op, ...details }); }
function admission(s) {
    demand(s.pro && !s.billingBlocked, 'Paid cloud starts are blocked; local work continues');
    demand(s.meterFresh, 'Meter is stale; reconcile before new admission');
    demand(!s.reservation, 'One live VM per account; existing reservation must settle first');
    demand(s.draft.repoAuthorized && REPOSITORIES.includes(s.draft.repository), 'Repository selection is not authorized');
    demand(s.draft.modelAvailable && s.draft.modelConsent, 'Model credentials or consent are not ready');
    const q = quote(s); demand(q.permitted, 'Worst-case reservation exceeds included credit or accepted cap');
    return q;
}
function reserve(s, requestId) {
    const q = admission(s); s.reservation = { requestId, seconds: q.seconds, cents: q.cents, bufferSeconds: q.bufferSeconds };
}
function beginStop(s, reason) {
    demand(s.cloud && !['deleted', 'stopped'].includes(s.cloud.status), 'No running compute to stop');
    s.cloud.status = 'stopping'; s.cloud.stopReason = reason; s.cloud.preview = false;
    s.message = 'Stop requested. Compute and reservation remain pending until provider confirmation (fixture).';
}
function pendingUsage(s) { return Object.values(s.pendingIntervals).some(interval => interval.status === 'pending'); }
function recordInterval(s, eventId, seconds) {
    if (s.meterEvents.includes(eventId)) return;
    s.meterEvents.push(eventId); s.cloud.elapsedSeconds += seconds;
    if (s.settledRuns[s.cloud.runId]) {
        const settled = s.settledRuns[s.cloud.runId];
        const units = seconds * SIZES[s.cloud.size].multiplier;
        const before = s.consumedSeconds; s.consumedSeconds += units;
        const extra = Math.max(0, s.consumedSeconds - TERMS.includedSeconds) - Math.max(0, before - TERMS.includedSeconds);
        const cents = Math.min(Math.ceil(extra * SIZES.standard.cents / 3600), Math.max(0, settled.reservationCents - settled.cents), Math.max(0, s.accountCapCents - s.consumedCents));
        s.consumedCents += cents; settled.cents += cents;
    } else {
        const remaining = s.reservation.seconds / SIZES[s.cloud.size].multiplier;
        if (s.cloud.elapsedSeconds >= remaining - TERMS.shutdownSeconds || s.cloud.elapsedSeconds >= s.draft.hours * 3600) beginStop(s, 'runtime or budget ceiling');
    }
}
function expire(s, effectiveAt = s.paidThrough) {
    if (!s.pro) return;
    s.asOf = effectiveAt;
    s.pro = false; s.entitlementVersion++;
    s.exportUntil = new Date(Date.parse(effectiveAt) + 14 * 86400000).toISOString().replace('.000Z', 'Z');
    if (s.cloud && !['stopped', 'deleted'].includes(s.cloud.status)) beginStop(s, 'entitlement ended');
    s.message = `Free local work continues. Cloud export only until ${s.exportUntil}; paid routes are closed.`;
}
export function applyPaidAction(original, action) {
    demand(original.fixture === true && original.version === FIXTURE_VERSION, 'Paid fixture unavailable');
    demand(action && typeof action === 'object' && !Array.isArray(action), 'Expected typed fixture action');
    const op = action.op; demand(Object.hasOwn(allowedFields, op), 'Unknown fixture operation');
    demand(Object.keys(action).every(k => k === 'op' || allowedFields[op].includes(k)), 'Unknown action field');
    const s = clone(original);
    if (s.principal === 'phone') demand(['phone-request', 'attach', 'activity', 'keepalive', 'stop'].includes(op), 'Phone pairing does not grant billing, VM, repository or vault management');
    if (s.principal === 'phone' && ['stop','activity','keepalive'].includes(op)) {
        demand(s.cloud && s.phonePairedWings.includes(s.cloud.wingId), 'Phone is not paired with this cloud wing');
    }
    if (s.principal === 'operator') demand(['owner-grant', 'owner-revoke'].includes(op), 'Operator fixture is limited to audited entitlement grants');
    if (op.startsWith('owner-')) demand(s.principal === 'operator', 'An authenticated operator is required');
    if (['repo-connect','git-clone','git-push','vault-toggle','model-select','network-toggle','tool-add','cloud-pair','attach','preview-share'].includes(op)) {
        demand(s.pro && !s.billingBlocked, 'Paid workspace is export-only; configuration and live routes are blocked');
    }
    if (op === 'export') demand(s.pro || (s.exportUntil && Date.parse(s.asOf) < Date.parse(s.exportUntil)), 'Workspace export grace expired');
    switch (op) {
    case 'intent': {
        demand(['reach', 'publish', 'cloud', 'share'].includes(action.moment), 'Unknown paid moment');
        text(action.task); demand(['lan', 'tailnet', 'relay'].includes(action.route), 'Unknown route');
        const offer = shouldOffer(s, action, true);
        s.intent = { moment: action.moment, task: action.task, route: action.route, wingId: 'fixture-local-wing', port: 3000, recipient: 'Alice' };
        if (offer) s.offersSeen.push(action.moment);
        s.message = offer ? 'Saved your request. Review the complete proposal or keep working locally.' : 'Request saved. LAN, tailnet and local work stay free.';
        break;
    }
    case 'dismiss':
        demand(['reach', 'publish', 'cloud', 'share'].includes(action.moment), 'Unknown paid moment');
        if (!s.offersSeen.includes(action.moment)) s.offersSeen.push(action.moment);
        s.message = 'Offer dismissed; your draft and local work remain available.'; break;
    case 'checkout':
        id(action.requestId); demand(!s.pro, 'Already entitled; no second subscription');
        if (s.checkout && s.checkout.status !== 'abandoned') { s.message = 'Reusing the existing fixture checkout; no second subscription.'; break; }
        s.checkout = { id: action.requestId, status: 'pending', accountId: s.accountId };
        s.message = 'Fixture checkout handoff created. Waiting for a verified fixture payment event.'; break;
    case 'checkout-return': s.message = 'Return received; entitlement still depends on the verified payment event.'; break;
    case 'payment':
        id(action.eventId); id(action.checkoutId);
        demand(s.checkout?.id === action.checkoutId && s.checkout.accountId === s.accountId, 'Checkout does not belong to this account');
        demand(['confirmed', 'declined', 'abandoned', 'delayed'].includes(action.outcome), 'Unknown payment result');
        if (s.paymentEvents.includes(action.eventId)) { s.message = 'Duplicate payment event ignored.'; break; }
        s.paymentEvents.push(action.eventId);
        if (action.outcome === 'confirmed') {
            if (s.checkout.status !== 'confirmed') { s.pro = true; s.entitlementVersion++; }
            s.checkout.status = 'confirmed'; s.entitlementSource = 'fixture-payment';
            s.message = 'Pro confirmed in this fixture; continue your original action explicitly. No VM created.';
        } else if (s.checkout.status !== 'confirmed') {
            s.checkout.status = action.outcome === 'delayed' ? 'pending' : action.outcome;
            s.message = `Payment ${action.outcome}. Draft saved; no paid action executed.`;
        }
        break;
    case 'payment-check': s.message = `Fixture checkout: ${s.checkout?.status || 'not started'}. Entitlement version ${s.entitlementVersion}.`; break;
    case 'owner-grant':
        demand(action.accountId === s.accountId, 'Account ID does not match verified fixture account'); text(action.reason, 120);
        s.pro = true; s.entitlementSource = 'owner-grant'; s.entitlementVersion++;
        audit(s, op, { reason: action.reason }); s.message = 'Audited owner grant recorded for the exact fixture account ID.'; break;
    case 'owner-revoke': expire(s, s.asOf); audit(s, op, { reason: 'operator revocation' }); break;
    case 'cancel': demand(s.pro, 'No paid subscription'); s.cancellation = true; s.message = `Renewal cancelled. Pro remains active through ${s.paidThrough}.`; break;
    case 'expire': expire(s); break;
    case 'advance-time':
        demand(typeof action.at === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(action.at), 'Invalid fixture clock');
        demand(Number.isFinite(Date.parse(action.at)) && Date.parse(action.at) >= Date.parse(s.asOf) && Date.parse(action.at) <= Date.parse(AS_OF) + 366 * 86400000, 'Invalid fixture clock range');
        s.asOf = action.at;
        if (s.pro && Date.parse(s.asOf) >= Date.parse(s.paidThrough)) expire(s, s.paidThrough);
        s.asOf = action.at;
        s.message = 'Fixture clock advanced; paid expiry and export deadline remain enforced.'; break;
    case 'draft':
        demand(!s.reservation, 'Review changes after existing compute stops');
        if (action.task !== undefined) { text(action.task); s.draft.task = action.task; }
        if (action.workspace !== undefined) { demand(['project', 'docs'].includes(action.workspace), 'Unknown workspace'); s.draft.workspace = action.workspace; }
        if (action.size !== undefined) { demand(Object.hasOwn(SIZES, action.size), 'Unknown size'); s.draft.size = action.size; }
        if (action.hours !== undefined) { number(action.hours, 0.25, 24, 'hours'); s.draft.hours = action.hours; }
        if (action.capCents !== undefined) { number(action.capCents, 0, 20000, 'cap'); demand(Number.isInteger(action.capCents), 'Cap must use integer cents'); s.draft.capCents = action.capCents; }
        if (action.overage !== undefined) { demand(typeof action.overage === 'boolean', 'Invalid overage consent'); s.draft.overage = action.overage; }
        s.message = 'Cloud draft saved. No files, credentials or compute moved.'; break;
    case 'template-approve':
        s.template = { ...s.template, approved: true, size: s.draft.size, hours: s.draft.hours, capCents: s.draft.capCents };
        s.message = 'Owner approved this exact coordinator template and cap.'; break;
    case 'phone-request':
        demand(s.principal === 'phone', 'Use the paired phone fixture'); text(action.task);
        demand(action.templateId === s.template.id && s.phonePairedWings.includes(s.template.coordinatorWingId), 'Unknown or unpaired coordinator template');
        s.phoneRequest = { task: action.task, templateId: action.templateId, wingId: s.template.coordinatorWingId, sessionId: s.template.coordinatorSessionId, status: s.template.approved ? 'queued-for-coordinator' : 'awaiting-owner-approval' };
        s.message = s.template.approved ? 'Cloud request queued for the paired coordinator within its preapproved template. No VM API called.' : 'Draft saved for owner approval on the Mac. Spending cannot be raised here.'; break;
    case 'repo-connect':
        demand(REPOSITORIES.includes(action.repository), 'Repository is outside the selected installation');
        demand(typeof action.branch === 'string' && /^cloud\/[a-zA-Z0-9_-]{1,60}$/.test(action.branch), 'Use a review branch under cloud/');
        s.draft.repository = action.repository; s.draft.branch = action.branch; s.draft.repoAuthorized = true; s.draft.cloned = false;
        s.message = 'Selected-repository fixture authorization saved. No PAT, SSH key or GitHub call.'; break;
    case 'git-clone': demand(s.fault !== 'disk', 'Disk full: writes paused; export available'); demand(s.draft.repoAuthorized, 'Repository permission missing'); s.draft.cloned = true; s.draft.gitStatus = 'cloned to retained workspace'; s.message = 'Fixture clone recorded; credential-free remote retained.'; break;
    case 'git-push':
        demand(s.fault !== 'disk', 'Disk full: writes paused; export available');
        demand(s.draft.cloned, 'Clone the authorized repository first'); demand(action.branch === s.draft.branch && action.branch !== 'main', 'Only the selected review branch is allowed');
        s.draft.gitStatus = 'review branch pushed; fixture PR ready'; s.message = 'Fixture PR prepared for review; no public or private GitHub write occurred.'; break;
    case 'vault-toggle':
        demand(Object.hasOwn(s.draft.vault, action.name), 'Unknown secret name'); demand(typeof action.enabled === 'boolean', 'Invalid secret permission');
        s.draft.vault[action.name] = action.enabled; s.message = 'Secret-name permission saved. Fixture contains no secret values.'; break;
    case 'model-select':
        demand(['claude', 'codex'].includes(action.agent), 'Unsupported adapter'); demand(action.mode === 'byo', 'Wingthing-billed inference is later scope');
        demand(action.consent === true, 'Explicit model context consent required');
        s.draft.agent = action.agent; s.draft.mode = action.mode; s.draft.modelConsent = true; s.draft.modelAvailable = true;
        s.message = 'BYO model fixture selected. Provider billing is separate; desktop subscriptions are not assumed reusable.'; break;
    case 'network-toggle':
        demand(action.domain === 'api.example.com', 'Only the fixture extra destination can be changed'); demand(typeof action.enabled === 'boolean', 'Invalid network consent');
        s.draft.network = s.draft.network.filter(d => d !== action.domain); if (action.enabled) s.draft.network.push(action.domain);
        s.message = 'Fixture destination policy saved. Inbound remains private; host enforcement is not claimed.'; break;
    case 'tool-add': demand(['Rust (reviewed image step)'].includes(action.tool), 'Unreviewed system package'); if (!s.draft.tools.includes(action.tool)) s.draft.tools.push(action.tool); s.message = 'Reviewed fixture image step recorded; workspace dependencies remain separate.'; break;
    case 'launch': {
        id(action.requestId); const fingerprint = JSON.stringify(s.draft);
        if (s.launchRecords[action.requestId]) {
            demand(s.launchRecords[action.requestId].fingerprint === fingerprint, 'Request ID conflicts with a changed draft');
            s.message = `Existing launch ${s.launchRecords[action.requestId].vmId} reconciled; no second allocation.`; break;
        }
        demand(!s.resumeRecords[action.requestId], 'Request ID already belongs to a restart');
        demand(!s.cloud || s.cloud.status === 'deleted', 'Use resume for an existing retained workspace'); reserve(s, action.requestId);
        const sequence = s.nextCloud++;
        s.cloud = { id: `fixture-vm-${sequence}`, volumeId: `fixture-volume-${sequence}`, wingId: `fixture-cloud-wing-${sequence}`, sessionId: `fixture-cloud-session-${sequence}`, runId: `fixture-run-${++s.nextRun}`, requestId: action.requestId,
            status: 'allocating', phase: 1, size: s.draft.size, elapsedSeconds: 0, diskGB: 2.1, active: false, idleMinutes: 0, preview: false, checkpoint: null, attached: false };
        s.exportManifest = null; s.deletionReview = false;
        s.launchRecords[action.requestId] = { fingerprint, vmId: s.cloud.id }; s.message = 'Budget reserved before allocation. Same request ID governs every retry.'; break;
    }
    case 'launch-step':
        demand(s.pro && !s.billingBlocked, 'Paid cloud readiness is blocked by entitlement or unpaid invoice');
        demand(s.cloud && ['allocating', 'booting'].includes(s.cloud.status), 'No boot in progress');
        s.cloud.phase++; s.cloud.status = s.cloud.phase < 5 ? 'booting' : 'ready';
        if (s.cloud.status === 'ready') { demand(s.draft.modelAvailable && s.draft.repoAuthorized, 'Native readiness incomplete'); s.cloud.preview = true; }
        s.message = ['','','Workspace attached.','Image booting.','Native agent readiness checking.','Fixture cloud wing ready. Pre-ready time is absorbed.'][s.cloud.phase]; break;
    case 'launch-fail':
        demand(s.cloud && ['allocating', 'booting'].includes(s.cloud.status), 'No pending launch'); demand(['allocation', 'clone', 'credentials', 'boot'].includes(action.phase), 'Unknown failure phase');
        s.cloud.status = 'failed'; s.fault = action.phase; s.message = `${action.phase} failed. Original request retained; orphan shutdown pending, no verified ready charge.`; break;
    case 'launch-retry':
        demand(s.cloud?.status === 'failed', 'No failed launch'); demand(s.pro && !s.billingBlocked, 'Entitlement unavailable');
        s.cloud.status = 'booting'; s.cloud.phase = 2; s.fault = null; s.message = 'Reconciled the original allocation ID; retry uses its existing reservation.'; break;
    case 'cloud-pair': demand(action.wingId === s.cloud?.wingId && s.pro, 'Verify the exact cloud wing'); if (!s.phonePairedWings.includes(action.wingId)) s.phonePairedWings.push(action.wingId); s.message = 'Fixture confirmation pinned this cloud wing separately.'; break;
    case 'attach':
        demand(s.cloud?.status === 'ready' && s.pro && !s.billingBlocked, 'Cloud is not ready for attach');
        demand(action.wingId === s.cloud.wingId && action.sessionId === s.cloud.sessionId, 'Qualified cloud session identity does not match');
        if (s.principal === 'phone') demand(s.phonePairedWings.includes(action.wingId), 'Phone is not paired with this cloud wing');
        s.cloud.attached = true; s.cloud.active = true; s.message = `Fixture attached to ${action.wingId}/${action.sessionId}. No input replayed.`; break;
    case 'meter': {
        id(action.eventId); number(action.seconds, 0, 86400, 'meter seconds'); demand(Number.isInteger(action.seconds), 'Seconds must be an integer'); demand(typeof action.verified === 'boolean', 'Invalid interval confirmation');
        demand(s.cloud && ['ready', 'stopping'].includes(s.cloud.status), 'Only ready lifecycle intervals can be metered');
        if (s.meterEvents.includes(action.eventId)) { s.message = 'Duplicate usage interval ignored.'; break; }
        const existing = s.pendingIntervals[action.eventId];
        if (existing) demand(existing.seconds === action.seconds && existing.runId === s.cloud.runId, 'Usage event ID conflicts with the retained interval');
        if (!action.verified) {
            if (!existing) s.pendingIntervals[action.eventId] = {eventId:action.eventId,runId:s.cloud.runId,seconds:action.seconds,status:'pending'};
            s.meterFresh = !pendingUsage(s); s.message = 'Estimated interval retained pending explicit reconciliation; new starts blocked.'; break;
        }
        if (existing) existing.status = 'verified';
        recordInterval(s,action.eventId,action.seconds);
        s.meterFresh = !pendingUsage(s);
        if (s.cloud.status !== 'stopping') s.message = 'Verified ready interval recorded once. Models and outbound remain separate.';
        break;
    }
    case 'stop': beginStop(s, 'owner stop'); break;
    case 'stop-confirm': {
        id(action.eventId);
        if (s.stopEvents[action.eventId]) { s.message = `Existing shutdown ${s.stopEvents[action.eventId].runId} reconciled; no second settlement.`; break; }
        demand(s.cloud && s.cloud.status === 'stopping', 'No pending shutdown');
        demand(!s.meterEvents.includes(action.eventId), 'Stop event already applied'); s.meterEvents.push(action.eventId);
        const units = s.cloud.elapsedSeconds * SIZES[s.cloud.size].multiplier;
        const before = s.consumedSeconds; s.consumedSeconds += units;
        const extra = Math.max(0, s.consumedSeconds - TERMS.includedSeconds) - Math.max(0, before - TERMS.includedSeconds);
        const actual = Math.ceil(extra * SIZES.standard.cents / 3600);
        const cents = Math.min(actual, s.reservation?.cents || 0, Math.max(0, s.accountCapCents - s.consumedCents));
        s.consumedCents += cents; s.cloud.status = 'stopped'; s.cloud.preview = false; s.cloud.active = false;
        s.settledRuns[s.cloud.runId] = {reservationCents:s.reservation.cents,cents};
        s.stopEvents[action.eventId] = {runId:s.cloud.runId,volumeId:s.cloud.volumeId};
        s.reservation = null; s.meterFresh = s.meterFresh && !pendingUsage(s); s.cloud.checkpoint = { at: s.asOf, kind: 'disk', secrets: 'excluded', integrity: 'verified' };
        s.message = s.meterFresh ? 'Provider stop confirmed (fixture). Compute settled; disk and artifacts retained. Vendor overrun is absorbed.' : 'Provider stop confirmed (fixture). Estimated usage remains under review; invoice and new starts blocked.'; break;
    }
    case 'activity':
        demand(s.cloud?.status === 'ready', 'Compute is not ready'); demand(typeof action.active === 'boolean', 'Invalid activity'); number(action.minutes, 0, 1440, 'idle minutes');
        s.cloud.active = action.active; s.cloud.idleMinutes = action.active ? 0 : action.minutes; s.message = action.active ? 'Active agent progress prevents idle stop even without viewers.' : 'Idle evidence recorded; blocked input may become idle.'; break;
    case 'idle-check':
        demand(s.cloud?.status === 'ready', 'Compute is not ready');
        if (!s.cloud.active && s.cloud.idleMinutes >= TERMS.idleMinutes) beginStop(s, 'idle timeout');
        else s.message = !s.cloud.active && s.cloud.idleMinutes >= TERMS.idleMinutes - TERMS.warningMinutes ? 'Idle warning: checkpoint and stop in 2 minutes; keep working to reset.' : 'Agent is active or inside the idle window; compute keeps working.';
        break;
    case 'keepalive': demand(s.cloud?.status === 'ready', 'Compute is not ready'); s.cloud.idleMinutes = 0; s.cloud.active = true; s.message = 'Interaction reset idle timer; budget and runtime ceilings still apply.'; break;
    case 'resume': {
        id(action.requestId); demand(action.coldConsent === true, 'Explicit disk-backed restart consent required');
        const fingerprint = JSON.stringify({volumeId:s.cloud?.volumeId,draft:s.draft,coldConsent:action.coldConsent});
        if (s.resumeRecords[action.requestId]) {
            demand(s.resumeRecords[action.requestId].fingerprint === fingerprint, 'Resume request ID conflicts with a changed draft');
            s.message = `Existing restart ${s.resumeRecords[action.requestId].runId} reconciled; no second execution.`; break;
        }
        demand(!s.launchRecords[action.requestId], 'Request ID already belongs to an initial launch');
        demand(s.cloud?.status === 'stopped', 'Resume requires retained stopped compute'); demand(s.fault !== 'deleted-volume', 'Deleted volume cannot resume');
        reserve(s, action.requestId);
        s.cloud.status = 'booting'; s.cloud.phase = 2; s.cloud.requestId = action.requestId; s.cloud.elapsedSeconds = 0;
        s.cloud.size = s.draft.size;
        s.cloud.previousRunId = s.cloud.runId; s.cloud.runId = `fixture-run-${++s.nextRun}`; s.cloud.active = false; s.cloud.attached = false;
        s.resumeRecords[action.requestId] = {fingerprint,runId:s.cloud.runId,volumeId:s.cloud.volumeId};
        s.message = 'Disk-backed restart admitted with a fresh reservation and runtime credentials. Old run ID preserved; prompt is not replayed.'; break;
    }
    case 'preview-share':
        demand(s.pro && s.cloud?.status === 'ready' && s.cloud.preview, 'Preview server is stopped or paid access ended'); demand(action.recipient === 'Alice', 'Select an explicit fixture recipient');
        s.cloud.previewRecipient = action.recipient; s.message = 'Fixture preview grant recorded for Alice only; nothing published.'; break;
    case 'export':
        demand(s.cloud && s.cloud.status !== 'deleted', 'No retained workspace to export');
        s.exportManifest = { volumeId: s.cloud.volumeId, files: ['source.patch', 'test-report.txt', 'tool-network-manifest.json'], excludes: ['vault values', 'device credentials', 'login homes', 'memory snapshots'], format: 'fixture-manifest-v1' };
        s.message = 'Export manifest prepared with selected edits and artifacts; credential material is excluded.'; break;
    case 'delete-review': demand(s.cloud?.status === 'stopped', 'Confirm compute shutdown before deleting disk'); s.deletionReview = true; s.message = 'Review deletion: type the exact volume ID. Recovery copies expire separately (5 / 7 day proposals).'; break;
    case 'delete':
        demand(s.deletionReview && s.cloud?.status === 'stopped', 'Review stopped workspace deletion first'); demand(action.volumeId === s.cloud.volumeId, 'Exact volume ID confirmation required');
        s.cloud.status = 'deleted'; s.cloud.preview = false; s.cloud.attached = false; s.phonePairedWings = s.phonePairedWings.filter(w => w !== s.cloud.wingId); s.deletionReview = false;
        s.message = 'Fixture workspace deleted; cloud routes and tokens revoked. Local device grants remain.'; break;
    case 'fault':
        demand(['cap', 'disk', 'network', 'restore', 'deleted-volume', 'stale-meter'].includes(action.kind), 'Unknown failure'); s.fault = action.kind;
        if (action.kind === 'cap' && s.cloud?.status === 'ready') beginStop(s, 'cap reached');
        if (action.kind === 'disk' && s.cloud) s.cloud.diskGB = 10;
        if (action.kind === 'stale-meter') s.meterFresh = false;
        s.message = { cap: 'Cap reached: compute stopping; disk retained; accepted cap cannot rise silently.', disk: 'Disk full: writes paused; export remains available.', network: 'Egress denied: api.example.com. Owner must review destination policy.', restore: 'Snapshot incompatible: explicit disk-backed restart consent required.', 'deleted-volume': 'Volume unavailable: cannot resume deleted data.', 'stale-meter': 'Usage estimated / under review. New admissions fail closed.' }[action.kind]; break;
    case 'recover-cold': demand(s.fault === 'restore' && action.consent === true, 'Review incompatible restore and consent to cold restart'); s.fault = null; s.message = 'Disk-backed recovery selected; no execution identity or prompt replay hidden.'; break;
    case 'account-cap':
        number(action.capCents, 0, 20000, 'account cap'); demand(Number.isInteger(action.capCents), 'Use integer cents');
        demand(action.capCents >= s.consumedCents + (s.reservation?.cents || 0), 'Cannot decrease below accrued charges and outstanding reservation');
        s.accountCapCents = action.capCents; s.message = 'Account cap saved for future admission; outstanding reservations remain enforced.'; break;
    case 'invoice-finalize':
        id(action.eventId); demand(action.invoiceId === s.invoice.id, 'Invoice ID mismatch'); demand(s.meterFresh && !s.reservation, 'Only reconciled, settled usage can be invoiced');
        if (s.invoiceEvents.includes(action.eventId) || s.invoice.status === 'finalized') { s.message = 'Duplicate invoice ignored.'; break; }
        s.invoiceEvents.push(action.eventId); s.invoice.status = 'finalized'; s.invoice.cents = TERMS.monthlyCents + s.consumedCents;
        s.invoice.lines = [{ label: 'Pro subscription (proposal)', cents: TERMS.monthlyCents }, { label: 'Verified compute overage after credits', cents: s.consumedCents }, { label: 'Included 10 GB workspace storage', cents: 0 }, { label: 'BYO model invoices billed externally', cents: 0 }];
        s.invoice.events = [...s.meterEvents]; s.message = 'Fixture invoice finalized from settled event IDs. BYO usage is not a Wingthing charge.'; break;
    case 'billing-fail':
        demand(action.invoiceId === s.invoice.id, 'Invoice ID mismatch'); s.invoice.status = 'unpaid'; s.billingBlocked = true;
        if (s.cloud && ['allocating','booting','ready','failed'].includes(s.cloud.status)) beginStop(s, 'unpaid invoice'); s.message = `Invoice ${s.invoice.id} unpaid. New starts blocked; stop policy applies; local work continues. Fixture deadline ${s.paidThrough}.`; break;
    case 'billing-recover': s.billingBlocked = false; s.invoice.status = 'paid'; s.message = 'Fixture payment-method recovery recorded. Cap unchanged; no automatic launch or duplicate charge.'; break;
    case 'notification': demand(Object.hasOwn(s.notifications, action.name) && typeof action.enabled === 'boolean', 'Unknown notification preference'); s.notifications[action.name] = action.enabled; s.message = 'Preference saved for the explicit fixture owner; no notification sent.'; break;
    case 'support':
        s.report = { accountId: s.accountId, requestId: s.cloud?.requestId || null, wingId: s.cloud?.wingId || null, version: 'paid-fixture-v1', route: 'fixture-local', classification: s.fault || (s.billingBlocked ? 'unpaid-invoice' : 'none'), usageEventIds: [...s.meterEvents], invoiceId: s.invoice.id };
        s.message = 'Metadata-only support report prepared for your review. Nothing sent.'; break;
    case 'reconcile': {
        id(action.eventId);
        if (pendingUsage(s) || action.usageEventId !== undefined) {
            id(action.usageEventId); demand(['confirmed','rejected'].includes(action.decision), 'Explicit interval reconciliation decision required');
            const interval = s.pendingIntervals[action.usageEventId]; demand(interval, 'Unknown retained usage interval');
            demand(interval.runId === s.cloud?.runId, 'Usage interval execution identity does not match');
            if (interval.status === 'pending') {
                interval.status = action.decision === 'confirmed' ? 'verified' : 'rejected';
                if (action.decision === 'confirmed') recordInterval(s, interval.eventId, interval.seconds);
            } else demand(interval.status === (action.decision === 'confirmed' ? 'verified' : 'rejected'), 'Usage interval already resolved with another decision');
        }
        s.meterFresh = !pendingUsage(s); audit(s, op, { eventId: action.eventId, usageEventId:action.usageEventId || null, decision:action.decision || 'source-rechecked' });
        s.message = 'Fixture meter reconciliation recorded with interval provenance; final invoices stay separate.'; break;
    }
    case 'team-check':
        demand(['sample-tenant', 'wrong-tenant'].includes(action.tenant) && ['member', 'admin', 'phone'].includes(action.role) && ['shared-project', 'billing'].includes(action.resource) && typeof action.revoked === 'boolean', 'Invalid team review');
        s.teamDecision = action.tenant === 'sample-tenant' && !action.revoked && action.role !== 'phone' && (action.resource !== 'billing' || action.role === 'admin') ? 'allowed by explicit fixture resource role' : 'denied; live access must close';
        s.message = `Later team design: ${s.teamDecision}. Individual Pro does not include team seats.`; break;
    }
    if (!op.startsWith('owner-') && op !== 'reconcile') audit(s, op);
    return s;
}
export function fixtureDownload(s, kind) {
    if (kind === 'invoice') return JSON.stringify({ fixture: true, ...s.invoice }, null, 2);
    if (kind === 'support') { demand(s.report, 'Prepare the support report first'); return JSON.stringify(s.report, null, 2); }
    if (kind === 'export') {
        demand(s.cloud && s.cloud.status !== 'deleted', 'Workspace was deleted');
        demand(s.pro || (s.exportUntil && Date.parse(s.asOf) < Date.parse(s.exportUntil)), 'Workspace export grace expired');
        demand(s.exportManifest, 'Prepare the export first'); return JSON.stringify(s.exportManifest, null, 2);
    }
    demand(kind === 'artifacts', 'Unknown artifact');
    return 'Dormant fixture artifact\nTest report: fixture contract exercised.\nPatch: review before applying.\nNo source, credentials or real provider results are included.\n';
}
