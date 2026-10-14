export const SEARCH_VERSION = 'wingthing.session-inventory.v1';

export function mergeSearchInventories(inventories, failures = []) {
    const rows = new Map(), sources = [...failures];
    let complete = failures.length === 0;
    for (const inventory of inventories) {
        complete = complete && inventory.complete === true;
        sources.push(...(inventory.sources || []));
        for (const incoming of inventory.sessions || []) {
            const key = JSON.stringify([incoming.origin_wing_id, incoming.session_id]);
            if (!incoming.origin_wing_id || !incoming.session_id) { complete = false; continue; }
            const prior = rows.get(key);
            let row = { ...incoming, routes: [...(incoming.routes || [])] };
            if (prior) {
                const latest = value => Math.max(0, ...value.routes.filter(route => route.healthy).map(route => Date.parse(route.observed_at) || 0));
                const fields = [...new Set([...(prior.match_fields || []), ...(row.match_fields || [])])];
                const incomplete = prior.search_incomplete || row.search_incomplete;
                const routes = new Map();
                for (const route of [...prior.routes, ...row.routes]) {
                    const routeKey = JSON.stringify([route.wing_id, route.kind, route.remote || '']);
                    const old = routes.get(routeKey);
                    if (!old || Date.parse(route.observed_at) >= Date.parse(old.observed_at)) routes.set(routeKey, route);
                }
                if (latest(prior) > latest(row)) row = { ...prior };
                row.routes = [...routes.values()];
                row.match_fields = fields;
                row.search_incomplete = incomplete;
            }
            rows.set(key, row);
        }
    }
    const sessions = [...rows.values()].sort((a, b) => Number(b.pinned && b.role === 'coordinator') - Number(a.pinned && a.role === 'coordinator'));
    if (sessions.length > 200) complete = false;
    return { sessions: sessions.slice(0, 200), sources, complete };
}

export function searchRoute(row, wings, now = Date.now()) {
    if (!row.origin_wing_id || !row.session_id) return null;
    return [...(row.routes || [])].filter(route => {
        const age = now - Date.parse(route.observed_at);
        return route.healthy && age >= 0 && age <= 30000 &&
            (route.kind === 'via' || (route.kind === 'direct' && route.wing_id === row.origin_wing_id)) &&
            wings.some(wing => wing.wing_id === route.wing_id && wing.online !== false);
    }).sort((a, b) => Number(a.kind !== 'direct') - Number(b.kind !== 'direct'))[0] || null;
}

// Each input change retires every earlier request, even before debounce fires.
export function createSearchController(search, update, now = Date.now) {
    let generation = 0;
    const previous = new Map();
    function publishResult(result,query) {
        const saved=previous.get(query);
        if(result.complete===false&&!result.sessions?.length&&saved){
            update({...result,loading:false,query,cached:true,fetchedAt:saved.fetchedAt,sessions:saved.sessions.map(row=>({...row,routes:(row.routes||[]).map(route=>({...route,healthy:false}))}))});return;
        }
        const value={...result,loading:false,query,fetchedAt:now()};
        if(result.complete===true){previous.delete(query);previous.set(query,value);while(previous.size>4)previous.delete(previous.keys().next().value);}
        update(value);
    }
    return {
        retire(clear = false) { generation++; if(clear)previous.clear(); },
        async run(query) {
            const ticket = ++generation;
            update({ loading: true, query });
            try { const result=await search(query);if(ticket===generation)publishResult(result,query); }
            catch(error){if(ticket===generation)publishResult({sessions:[],sources:[],complete:false,error:error.message},query);}
        },
    };
}

// Checked emptiness differs from missing coverage. Keep both observable even
// before the first successful fleet row has arrived.
export function inventoryPresentation(state) {
    const checked = state.complete === true || state.complete === false;
    const visible = Boolean(state.query || state.loading || state.error || state.sessions?.length || checked);
    const fallback = !state.query && !state.sessions?.length && (state.loading || state.complete === false || state.error);
    return { visible, fallback, status: state.loading ? (state.query ? 'Searching sessions…' : 'Loading machine inventory…') :
        state.cached ? `Search unavailable · ${state.sessions?.length || 0} cached sessions · coverage incomplete` : state.error || (checked ? state.complete===false&&!state.sessions?.length ? 'Search unavailable · coverage incomplete' : `${state.sessions?.length || 0} sessions · ${state.complete ? 'coverage complete' : 'coverage incomplete'}` : '') };
}

export function sourceProblems(sources=[]) {
    const grouped=new Map();
    for(const source of sources.filter(s=>!s.healthy)){
        const key=source.wing_id||source.machine_label||'unknown',prior=grouped.get(key);
        if(!prior)grouped.set(key,{...source});
        else if(source.error&&!String(source.error).startsWith('Configured'))grouped.set(key,{...prior,...source});
    }
    return [...grouped.values()];
}
