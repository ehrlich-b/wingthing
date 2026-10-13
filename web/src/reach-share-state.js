// Non-authoritative planned product state. This module never opens a route,
// issues a credential, sends input, contacts an identity provider or runs SSH.
export function dormantPreviewEnabled(user) {
    return !!user && user.reach_share_preview === true;
}

export function selectReachRoute(wing, network) {
    if (!wing.pinMatches) return { route: null, reason: 'Wing fingerprint changed. Verify locally before reconnecting.' };
    if (wing.revoked) return { route: null, reason: 'Device revoked on this wing. All routes refuse access.' };
    if (!wing.awake) return { route: null, reason: 'Wing unavailable. Wake the machine and retry; process state is unknown.' };
    if (network.lan) return { route: 'LAN', reason: 'Direct LAN · Free' };
    if (network.tailnet) return { route: 'tailnet', reason: 'Direct tailnet · Free' };
    if (!wing.registered) return { route: null, reason: 'No direct route. Owner registration is required for the planned relay.' };
    if (!wing.pro) return { route: null, reason: 'Relay entitlement expired. Valid LAN and tailnet pairing stay free.' };
    if (!network.relay) return { route: null, reason: 'Relay interrupted. Retry this wing or return to LAN / tailnet.' };
    return { route: 'relay', reason: 'Relay · encrypted to the pinned wing · Dormant' };
}

export class ReachPreview {
    constructor() {
        this.wings = new Map();
        this.pending = null;
        this.submissions = 0;
    }
    wing(id) {
        if (!id) throw new Error('Select an exact wing');
        if (!this.wings.has(id)) this.wings.set(id, { registered: false, pro: true, awake: true, pinMatches: true, revoked: false });
        return this.wings.get(id);
    }
    register(id) { this.wing(id).registered = true; }
    beginInput(wingId, sessionId, text, intentId) {
        if (this.pending) throw new Error('Check the original receipt before another input');
        if (!wingId || !sessionId || !intentId || !text.trim()) throw new Error('Exact wing, session and input are required');
        this.pending = Object.freeze({ wingId, sessionId, text, intentId, status: 'delivery unknown' });
        this.submissions++;
        return this.pending;
    }
    reconnect() { return this.pending; } // Observation only. No send callback exists.
    checkReceipt(receipt) {
        if (!this.pending || receipt.wingId !== this.pending.wingId || receipt.sessionId !== this.pending.sessionId || receipt.intentId !== this.pending.intentId) throw new Error('Receipt does not match the original input');
        if (!['acknowledged', 'not found', 'delivery unknown'].includes(receipt.status)) throw new Error('Invalid receipt');
        // A missing receipt is not proof that input was not delivered.
        if (receipt.status === 'acknowledged') this.pending = Object.freeze({ ...this.pending, status: 'acknowledged' });
        return this.pending;
    }
}

export class SharePreview {
    constructor(owner) {
        this.owner = owner;
        this.resources = new Map();
        this.grants = new Map();
        this.sequence = 0;
    }
    requireOwner(actor) { if (!actor || actor !== this.owner) throw new Error('Owner authorization required'); }
    publish(actor, wingId, port) {
        this.requireOwner(actor);
        if (!wingId || !Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Select an exact wing and valid port');
        const key = JSON.stringify([wingId, 'app', port]);
        if (!this.resources.has(key)) this.resources.set(key, { id: 'preview-' + (++this.sequence), key, wingId, kind: 'app', port, up: true, active: true });
        return this.resources.get(key);
    }
    session(actor, wingId, sessionId) {
        this.requireOwner(actor);
        if (!wingId || !sessionId) throw new Error('Exact session required');
        const key = JSON.stringify([wingId, 'session', sessionId]);
        if (!this.resources.has(key)) this.resources.set(key, { id: 'session-' + (++this.sequence), key, wingId, sessionId, kind: 'session', active: true });
        return this.resources.get(key);
    }
    invite(actor, key, identity, role) {
        this.requireOwner(actor);
        const resource = this.resources.get(key);
        if (!resource || !resource.active) throw new Error('Resource unavailable');
        if (!identity || !['google', 'github'].includes(identity.provider) || !identity.subject) throw new Error('Choose a verified fixture identity');
        if ((resource.kind === 'app' && role !== 'view') || (resource.kind === 'session' && role !== 'observer')) throw new Error('Separate controller authorization is required');
        const grantKey = JSON.stringify([key, identity.provider, identity.subject]);
        const grant = { key: grantKey, resourceKey: key, identity: { ...identity }, role, active: true, streams: 0 };
        this.grants.set(grantKey, grant);
        return grant;
    }
    access(key, identity, pro = true) {
        const resource = this.resources.get(key);
        const grant = identity && this.grants.get(JSON.stringify([key, identity.provider, identity.subject]));
        // Denied callers receive no resource or machine metadata.
        if (!resource || !resource.active || !pro || !grant || !grant.active) return { status: 'denied', message: 'Access ended or this identity was not invited. Ask the owner for a new grant.' };
        if (resource.kind === 'app' && !resource.up) return { status: 'unavailable', message: 'Application unavailable. The owner needs to check the local app.' };
        return { status: 'allowed', resource: { ...resource }, role: grant.role, canInput: false };
    }
    open(key, identity) {
        const result = this.access(key, identity);
        if (result.status === 'allowed') this.grants.get(JSON.stringify([key, identity.provider, identity.subject])).streams++;
        return result;
    }
    revoke(actor, key) {
        this.requireOwner(actor);
        const grant = this.grants.get(key);
        if (!grant) throw new Error('Grant not found');
        grant.active = false;
        const closed = grant.streams;
        grant.streams = 0;
        return closed;
    }
}

export function connectionMeasurement(start, end, route, measuredAt) {
    if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) throw new Error('Invalid measurement');
    return { ms: Math.round(end - start), route, measuredAt };
}

// Accounts used only by the existing, gated local preview.
export function previewIdentity(provider) {
    const accounts = {
        google: { provider: 'google', subject: 'sample-google-alice-42', name: 'Alice', email: 'alice@example.test' },
        github: { provider: 'github', subject: 'sample-github-alice-17', name: 'Alice', username: 'alice-example' },
    };
    if (!accounts[provider]) throw new Error('Choose a verified account');
    return { ...accounts[provider] };
}
export function accessReview(resource, identity, computer, sessionName = '') {
    const account = identity?.provider === 'google' ? identity.email : identity?.provider === 'github' && identity.username ? '@' + identity.username : '';
    if (!account || !identity.subject) throw new Error('Verify the recipient account before granting access');
    if (!resource || !computer) throw new Error('Select a computer and resource');
    return {
        recipient: `${identity.provider === 'google' ? 'Google' : 'GitHub'} · ${account} · account ${identity.subject}`,
        resource: resource.kind === 'app' ? `${computer} · ${resource.id} · port ${resource.port}` : `${computer} · ${sessionName || resource.id} · ${resource.sessionId}`,
        role: resource.kind === 'app' ? 'View app' : 'Observe session · no input',
    };
}
