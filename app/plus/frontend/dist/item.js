(() => {
    'use strict';

    const $ = (id) => document.getElementById(id);
    const nativeApp = () => window.go && window.go.plus && window.go.plus.ItemWindowApp;
    // A mintaadat csak a külön, böngészős felületellenőrzésben jelenik meg.
    const preview = new URLSearchParams(window.location.search).get('preview') === '1' && !nativeApp();
    const pendingOptions = new Set();
    let optionsRevision = 0;
    let optionsQueue = Promise.resolve();
    let localBusy = false;
    let changingTracking = null;
    let trackingFailure = null;
    let keyVisible = false;
    let keyFromStorage = false;
    let keyRevealRevision = 0;
    let keyRevealPending = false;
    let initialRender = true;
    let latestStateSequence = null;
    let loadedStockContext = '';
    let inventoryData = null;
    let inventoryContext = '';
    let inventoryRevision = 0;
    let inventoryLoading = false;
    let inventorySaving = false;
    let inventoryBackupBusy = false;
    let inventoryRowsRevision = 0;
    let inventoryControlsKey = '';
    let inventorySyncAttempt = '';
    let inventoryRefreshRequest = null;
    let inventorySyncErrorShown = false;
    let inventoryNeedsReload = false;
    let inventoryLoadedStamp = '';
    const inventoryNameCollator = new Intl.Collator('en', { sensitivity: 'base' });
    const shortcutErrors = new Map();
    let shortcutCaptureInput = null;
    let shortcutCapturePending = false;
    let shortcutCaptureRevision = 0;
    let shortcutCaptureQueue = Promise.resolve();
    const stockActions = [
        { id: 'add-fir', property: 'addFIRHotkey', fallback: 'F1', ready: 'addFIRHotkeyReady', error: 'addFIRHotkeyError',
            allowed: 'addFIRAllowed', quantity: 'firQuantity', method: 'AddFIRTrackedItem', label: 'Add 1 Found in Raid item' },
        { id: 'add-regular', property: 'addRegularHotkey', fallback: 'F2', ready: 'addRegularHotkeyReady', error: 'addRegularHotkeyError',
            allowed: 'addRegularAllowed', quantity: 'regularQuantity', method: 'AddRegularTrackedItem', label: 'Add 1 Regular item' },
        { id: 'remove-fir', property: 'removeFIRHotkey', fallback: 'F3', ready: 'removeFIRHotkeyReady', error: 'removeFIRHotkeyError',
            allowed: 'removeFIRAllowed', quantity: 'firQuantity', method: 'RemoveFIRTrackedItem', label: 'Remove 1 Found in Raid item', removing: true },
        { id: 'remove-regular', property: 'removeRegularHotkey', fallback: 'F4', ready: 'removeRegularHotkeyReady', error: 'removeRegularHotkeyError',
            allowed: 'removeRegularAllowed', quantity: 'regularQuantity', method: 'RemoveRegularTrackedItem', label: 'Remove 1 Regular item', removing: true },
    ];
    const shortcutFields = stockActions.map((action) => ({ ...action, id: action.id + '-hotkey' }));
    let state = {
        enabled: true, automatic: true, language: 'en', mode: 'PVP', hasAPIKey: false,
        addFIRHotkey: 'F1', addFIRHotkeyReady: false, addFIRHotkeyError: '', hotkeyCapture: false,
        addRegularHotkey: 'F2', addRegularHotkeyReady: false, addRegularHotkeyError: '',
        removeFIRHotkey: 'F3', removeFIRHotkeyReady: false, removeFIRHotkeyError: '',
        removeRegularHotkey: 'F4', removeRegularHotkeyReady: false, removeRegularHotkeyError: '',
        busy: false, status: 'no_key', error: '', ocrLanguages: [], cachedAt: '', stale: false, item: null, scanDetails: null,
        characterUid: '', characters: [], charactersLoading: false, charactersError: '', tracking: null,
        local: null, pendingAdds: null,
    };

    const statusNames = {
        idle: 'Waiting for an item Inspect window.', ready: 'Waiting for an item Inspect window.', disabled: 'Automatic scanning is off.',
        scanning: 'Inspect title scanning…', searching: 'Searching for item…', no_key: 'API key required',
        no_match: 'Item not found', waiting: 'Waiting for the game',
    };

    const isBusy = () => localBusy || !!state.busy;
    const safeText = (value) => typeof value === 'string' ? value.slice(0, 500) : '';
    const progressContext = () => JSON.stringify([state.characterUid, String(state.mode || '').toUpperCase(), optionsRevision]);
    const normalizedHotkey = (value, fallback = 'F1') => {
        if (typeof value !== 'string') return fallback;
        if (['off', 'disabled'].includes(value.trim().toLowerCase())) return 'off';
        const parts = value.trim().split('+');
        const keyText = parts.pop().toUpperCase();
        const namedKeys = { SPACE: 'Space', INSERT: 'Insert', DELETE: 'Delete', HOME: 'Home', END: 'End', PAGEUP: 'PageUp', PAGEDOWN: 'PageDown' };
        const key = /^(F([1-9]|1[0-2])|[A-Z0-9])$/.test(keyText) ? keyText : namedKeys[keyText];
        const modifiers = parts.map((part) => part.toLowerCase());
        if (!key || modifiers.some((part) => !['ctrl', 'alt', 'shift'].includes(part)) || new Set(modifiers).size !== modifiers.length) return fallback;
        return [...['ctrl', 'alt', 'shift'].filter((part) => modifiers.includes(part)).map((part) =>
            part[0].toUpperCase() + part.slice(1)), key].join('+');
    };
    const shortcutDisplay = (value) => value === 'off' ? 'Disabled' : value;
    const hotkeyFromEvent = (event) => {
        if (event.metaKey || (event.code || '').startsWith('Numpad')) return '';
        const code = event.code || '';
        const virtualKey = event.keyCode;
        const namedVirtualKeys = { 32: 'Space', 45: 'Insert', 46: 'Delete', 36: 'Home', 35: 'End', 33: 'PageUp', 34: 'PageDown' };
        let key;
        // A Windows Chromium keyCode a natív hook által használt VK értékeket adja.
        if (Number.isInteger(virtualKey) && virtualKey > 0) {
            key = virtualKey >= 65 && virtualKey <= 90 || virtualKey >= 48 && virtualKey <= 57 ? String.fromCharCode(virtualKey) :
                virtualKey >= 112 && virtualKey <= 123 ? 'F' + (virtualKey - 111) : namedVirtualKeys[virtualKey];
            if (!key) return '';
        } else {
            key = event.key === ' ' ? 'Space' : event.key;
            if (event.shiftKey && /^Digit[0-9]$/.test(code) && typeof key === 'string' && /^[\x21-\x2f\x3a-\x40\x5b-\x60\x7b-\x7e]$/.test(key)) key = code.slice(5);
        }
        return normalizedHotkey([event.ctrlKey ? 'Ctrl' : '', event.altKey ? 'Alt' : '', event.shiftKey ? 'Shift' : '', key]
            .filter(Boolean).join('+'), '');
    };
    const positiveNumber = (value) => typeof value === 'number' && Number.isFinite(value) && value > 0;
    const numberText = (value) => Math.round(value).toLocaleString('en-US');
    const localDateText = (value) => {
        if (typeof value !== 'string' || !value.trim()) return '';
        const date = new Date(value);
        if (!Number.isFinite(date.getTime())) return '';
        const year = date.getFullYear();
        if (year < 0 || year > 9999) return '';
        const pad = (number, length = 2) => String(number).padStart(length, '0');
        return pad(year, 4) + '.' + pad(date.getMonth() + 1) + '.' + pad(date.getDate()) + '. ' +
            pad(date.getHours()) + ':' + pad(date.getMinutes()) + ':' + pad(date.getSeconds());
    };
    const priceText = (value, currency = '₽') => {
        if (!positiveNumber(value) || !currency) return '—';
        const symbol = ({ RUB: '₽', RUR: '₽', Rouble: '₽', rub: '₽', USD: '$', Dollar: '$', usd: '$', EUR: '€', Euro: '€', eur: '€' })[currency] || safeText(currency);
        return symbol ? numberText(value) + '\u00a0' + symbol : '—';
    };
    const renderPrice = (priceID, slotID, value, slots, currency = '₽') => {
        const total = priceText(value, currency);
        $(priceID).textContent = total;
        $(slotID).textContent = slots && positiveNumber(value) ? priceText(value / slots, currency) : '—';
        $(slotID).parentElement.classList.toggle('hidden', total === '—');
    };
    const englishModelAvailable = () => (state.ocrLanguages || []).some((value) => {
        const name = String(value).toLowerCase().replace('_', '-');
        return name === 'en' || name.startsWith('en-') || name === 'eng';
    });
    const safeImageURL = (value) => {
        try {
            const url = new URL(value);
            return url.protocol === 'https:' && url.hostname === 'cdn.tarkov-market.app' &&
                !url.username && !url.password && (!url.port || url.port === '443') ? url.href : '';
        } catch (_) { return ''; }
    };
    const currentTracking = () => {
        const tracking = state.tracking;
        return state.hasAPIKey && safeText(state.characterUid) && tracking && typeof tracking === 'object' && state.item &&
            tracking.itemUid === state.item.uid && tracking.charUid === state.characterUid &&
            (tracking.itemId === state.item.bsgId || !safeText(tracking.itemId) && tracking.loading && tracking.tracked !== true) ? tracking : null;
    };
    const sameTrackingTarget = (target) => !!target && !!state.item &&
        target.itemUid === state.item.uid && target.charUid === state.characterUid;
    const trackingEntries = () => {
        const tracking = currentTracking();
        if (!tracking) return [];
        return Array.isArray(tracking.variants) && tracking.variants.length ? tracking.variants.filter((variant) =>
            variant && typeof variant === 'object' && sameTrackingTarget(variant)) : [tracking];
    };
    const stockTrackingContext = (tracking) => {
        const mode = String(state.mode || '').toUpperCase();
        if (!state.hasAPIKey || !tracking || tracking.tracked !== true || !sameTrackingTarget(tracking) ||
            !safeText(state.item && state.item.uid) || !safeText(state.item && state.item.bsgId) || tracking.itemId !== state.item.bsgId ||
            !['PVP', 'PVE', 'SEASON'].includes(mode) ||
            ![tracking.availableQuantity, tracking.requiredQuantity, tracking.firQuantity, tracking.regularQuantity].every((value) => Number.isSafeInteger(value) && value >= 0) ||
            tracking.firQuantity + tracking.regularQuantity > tracking.availableQuantity) return '';
        return JSON.stringify([state.item.uid, state.item.bsgId, state.characterUid, mode]);
    };
    const canChangeTracking = (tracking, action) => {
        const current = currentTracking();
        const stockAction = stockActions.find((entry) => entry.id === action);
        const context = stockTrackingContext(tracking);
        const refreshing = !!tracking && (tracking.loading || !!state.local && state.local.loading);
        if (state.hotkeyCapture || shortcutCapturePending || !state.local || safeText(state.local.error) || !context ||
            refreshing && context !== loadedStockContext) return false;
        return (!!nativeApp() || preview) && !isBusy() && !safeText(state.error) && state.hasAPIKey && !!state.characterUid &&
            !!safeText(state.item && state.item.uid) &&
            !!safeText(state.item && state.item.bsgId) &&
            !!tracking && sameTrackingTarget(tracking) && tracking.itemId === state.item.bsgId && tracking.tracked === true &&
            tracking.firInventorySeparate !== true &&
            !!stockAction && tracking[stockAction.allowed] === true &&
            (!stockAction.removing || Number.isSafeInteger(tracking[stockAction.quantity]) && tracking[stockAction.quantity] > 0) &&
            !tracking.adding && !safeText(tracking.error) && !sameTrackingTarget(trackingFailure) &&
            pendingOptions.size === 0 && !(current && Array.isArray(current.variants) && current.variants.length > 1);
    };
    const quantityText = (value) => Number.isInteger(value) && value >= 0 ? numberText(value) : '—';
    const itemsText = (value) => quantityText(value) + (value === 1 ? ' item' : ' items');
    const availableCharacters = () => (Array.isArray(state.characters) ? state.characters : []).filter((character) =>
        character && typeof character === 'object' && safeText(character.charUid));
    const characterMode = (character) => String(character && character.gameMode || '').toUpperCase();

    const renderCharacters = () => {
        const select = $('tracked-character');
        const characters = availableCharacters();
        const signature = JSON.stringify(characters.map((character) =>
            [safeText(character.charUid), safeText(character.name), characterMode(character)]));
        if (select.dataset.characters !== signature) {
            const selected = select.value;
            const placeholder = document.createElement('option');
            placeholder.value = '';
            placeholder.textContent = 'Select a character…';
            select.replaceChildren(placeholder, ...characters.map((character) => {
                const option = document.createElement('option');
                option.value = safeText(character.charUid);
                option.textContent = '(' + (safeText(character.name) || 'Unnamed character') + ') (' + (characterMode(character) || '—') + ')';
                return option;
            }));
            select.value = selected;
            select.dataset.characters = signature;
        }
        const error = safeText(state.charactersError);
        const notes = [];
        if (state.hasAPIKey && characters.length) {
            if (!state.characterUid) notes.push('Select a character to use Tracked Items.');
        }
        $('characters-note').textContent = error || (state.charactersLoading ? 'Loading Tracked Items characters…' :
            state.hasAPIKey && !characters.length ? 'No characters available.' :
                notes.join(' '));
        $('characters-note').classList.toggle('hidden', !$('characters-note').textContent);
        $('characters-note').classList.toggle('error', !!error);
    };

    const copyName = async (name) => {
        if (!name) return;
        showFeedback('');
        try {
            if (window.runtime && typeof window.runtime.ClipboardSetText === 'function') {
                if (await window.runtime.ClipboardSetText(name) !== true) throw new Error('Copy failed');
            } else if (!nativeApp() && navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
                await navigator.clipboard.writeText(name);
            } else {
                throw new Error('Clipboard unavailable');
            }
        } catch (_) {
            showFeedback('The name could not be copied.', true);
        }
    };
    const copyNameButton = (name, kind) => {
        const button = document.createElement('button');
        button.className = 'name-copy-button';
        button.type = 'button';
        button.title = kind === 'item' ? 'Copy item name' : kind === 'quest' ? 'Copy quest name' : 'Copy hideout name';
        button.setAttribute('aria-label', 'Copy ' + name);
        button.innerHTML = '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M5 5h8v8H5ZM10 5V2H2v8h3" /></svg>';
        button.addEventListener('click', () => copyName(name));
        return button;
    };
    const trackingCards = new Map();
    const trackingDisplayBaselines = new WeakMap();
    const nextItemTargetSignatures = new WeakMap();
    let nextItemCacheKey = '';
    let nextItemCache = null;
    const nextItemContextKey = () => {
        const itemUid = safeText(state.item && state.item.uid);
        const itemId = safeText(state.item && state.item.bsgId);
        const charUid = safeText(state.characterUid);
        const key = state.hasAPIKey && itemUid && itemId && charUid &&
            !pendingOptions.has('game-mode') && !pendingOptions.has('tracked-character') ?
            JSON.stringify([itemUid, itemId, charUid, String(state.mode || '').toUpperCase()]) : '';
        if (key !== nextItemCacheKey) {
            nextItemCacheKey = key;
            nextItemCache = null;
        }
        return key;
    };
    const parsedNextItem = (tracking, transient) => {
        const forecast = tracking.nextItem && typeof tracking.nextItem === 'object' && !Array.isArray(tracking.nextItem) ? tracking.nextItem : null;
        if (forecast && ['estimated', 'uncertain', 'unavailable'].includes(forecast.status)) {
            const targets = Array.isArray(forecast.targets) ? forecast.targets.filter((target) =>
                target && typeof target === 'object' && !Array.isArray(target) &&
                (target.kind === 'quest' || target.kind === 'module') && Number.isSafeInteger(target.requiredQuantity) && target.requiredQuantity >= 0 &&
                (target.allocationUnknown === true || Number.isSafeInteger(target.availableQuantity) && target.availableQuantity >= 0)).map((target) => ({
                    kind: target.kind, name: safeText(target.goalName).trim() || (target.kind === 'quest' ? 'Unnamed quest' : 'Unnamed hideout upgrade'),
                    availableQuantity: target.availableQuantity, requiredQuantity: target.requiredQuantity, allocationUnknown: target.allocationUnknown === true,
                    completed: target.completed === true,
                })).sort((a, b) => a.kind === b.kind ? 0 : a.kind === 'quest' ? -1 : 1) : [];
            if (targets.length) {
                const completedState = !transient && tracking.addFIRAllowed === false && tracking.addRegularAllowed === false &&
                    tracking.requiredQuantity === 0 && tracking.availableQuantity === 0 && targets.every((target) => target.completed);
                return {targets, completedState};
            }
        }
        return null;
    };
    const currentNextItem = (tracking, multipleVariants) => {
        const key = nextItemContextKey();
        const current = currentTracking();
        const typedVariant = multipleVariants && tracking && tracking.qualityKnown === true;
        const pending = state.pendingAdds && state.pendingAdds.running === true && Number.isSafeInteger(state.pendingAdds.count) && state.pendingAdds.count > 0;
        const transient = !!tracking && (tracking.loading || tracking.adding || !!state.local && state.local.loading ||
            isBusy() || pending || sameTrackingTarget(changingTracking));
        const invalid = !key || !tracking || !sameTrackingTarget(tracking) ||
            safeText(tracking.itemId) && tracking.itemId !== state.item.bsgId || tracking.firInventorySeparate === true ||
            !typedVariant && (multipleVariants || current && Array.isArray(current.variants) && current.variants.length > 1);
        const error = safeText(state.error) || safeText(state.local && state.local.error) ||
            !typedVariant && safeText(tracking && tracking.error) || sameTrackingTarget(trackingFailure);
        if (invalid || error) {
            if (!multipleVariants) nextItemCache = null;
            return null;
        }
        // Az egyértelmű variánsok lokális céljai külön DTO-ból jönnek;
        // soha nem osztozhatnak a normál tárgy közös megjelenítési cache-én.
        if (typedVariant) return parsedNextItem(tracking, transient);
        if (transient && nextItemCache) return nextItemCache;
        const parsed = parsedNextItem(tracking, transient);
        if (parsed) nextItemCache = parsed;
        else if (!transient) nextItemCache = null;
        return nextItemCache;
    };
    const renderNextItem = (panel, forecast) => {
        const nextItem = panel.querySelector('[data-tracked="next-item"]');
        const label = forecast && forecast.completedState ? 'Completed Item' : 'Next Item';
        panel.querySelector('[data-tracked="next-item-label"]').textContent = label;
        panel.querySelector('.tracked-stock-list').classList.toggle('hidden', !!(forecast && forecast.completedState));
        stockActions.forEach((action) => panel.querySelector('[data-tracked="' + action.id + '"]').classList.toggle('hidden', !!(forecast && forecast.completedState)));
        nextItem.classList.toggle('completed', !!(forecast && forecast.completedState));
        const baseline = trackingDisplayBaselines.get(panel);
        if (baseline) {
            panel.querySelector('[data-tracked="ratio"]').textContent = forecast && forecast.completedState ? '0/0' : baseline.stockRatio;
            panel.querySelector('[data-tracked="requirements"]').classList.toggle('hidden', !!(forecast && forecast.completedState) || !baseline.hasRequirements);
        }
        const targetSignature = JSON.stringify(forecast ? forecast.targets : []);
        if (nextItemTargetSignatures.get(panel) !== targetSignature) {
            nextItemTargetSignatures.set(panel, targetSignature);
            panel.querySelector('[data-tracked="next-item-targets"]').replaceChildren(...(forecast ? ['quest', 'module'].flatMap((kind) => {
                const targets = forecast.targets.filter((target) => target.kind === kind);
                if (!targets.length) return [];
                const group = document.createElement('div');
                group.className = 'tracked-next-item-target-group';
                const heading = document.createElement('h3');
                heading.className = 'tracked-next-item-target-heading';
                heading.textContent = kind === 'quest'
                    ? targets.length === 1 ? 'Quest' : 'Quests'
                    : targets.length === 1 ? 'Hideout Module' : 'Hideout Modules';
                group.append(heading, ...targets.map((target) => {
                    const row = document.createElement('div');
                    row.className = 'tracked-next-item-target';
                    row.dataset.kind = target.kind;
                    const name = document.createElement('span');
                    name.className = 'tracked-next-item-target-name';
                    name.textContent = target.name;
                    name.append(document.createTextNode(' '), copyNameButton(target.name, target.kind));
                    if (target.completed) {
                        const check = document.createElement('span');
                        check.className = 'tracked-next-item-target-completed';
                        check.innerHTML = '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="m3 8 3 3 7-7" /></svg>';
                        check.setAttribute('role', 'img');
                        check.setAttribute('aria-label', 'Completed');
                        name.append(document.createTextNode(' '), check);
                    }
                    const ratio = document.createElement('span');
                    ratio.className = 'tracked-next-item-target-ratio';
                    ratio.textContent = '(' + (target.allocationUnknown ? '—' : quantityText(target.availableQuantity)) + '/' + quantityText(target.requiredQuantity) + ')';
                    row.append(name, ratio);
                    return row;
                }));
                return [group];
                }) : []));
        }
        nextItem.classList.toggle('hidden', !forecast);
        if (forecast) nextItem.setAttribute('aria-label', label);
        else nextItem.removeAttribute('aria-label');
    };
    const updateNextItem = (panel, tracking, multipleVariants) => renderNextItem(panel, currentNextItem(tracking, multipleVariants));
    const renderTrackingCard = (panel, tracking, multipleVariants, variantLabel) => {
        const element = (name) => panel.querySelector('[data-tracked="' + name + '"]');
        const tracked = !!tracking && tracking.tracked === true;
        const changing = sameTrackingTarget(changingTracking) || !!tracking && !!tracking.adding;
        const failure = sameTrackingTarget(trackingFailure) ? trackingFailure.message : '';
        const error = failure || safeText(tracking && tracking.error);
        const loading = !!tracking && (tracking.loading || !!state.local && state.local.loading);
        const status = failure ? '' : changing ? changingTracking && changingTracking.action.startsWith('remove-') ? 'Removing…' : 'Updating stock…' : loading ?
            tracked ? 'Refreshing Tracked Items data…' : 'Loading Tracked Items data…' : safeText(tracking && tracking.status);
        const requirements = tracked && Array.isArray(tracking.requirements) ? tracking.requirements.filter((requirement) =>
            requirement && (requirement.kind === 'fir' || requirement.kind === 'regular') &&
            Number.isInteger(requirement.requiredQuantity) && requirement.requiredQuantity >= 0).sort((a, b) =>
                a.kind === b.kind ? 0 : a.kind === 'fir' ? -1 : 1) : [];
        const noRequiredQuantity = tracked && tracking.requiredQuantity === 0;
        const visibleRequirements = requirements;
        const anyGroups = tracked && Array.isArray(tracking.anyGroups) ? tracking.anyGroups.filter((group) =>
            group && Number.isSafeInteger(group.requiredQuantity) && group.requiredQuantity > 0 &&
            Number.isSafeInteger(group.availableQuantity) && group.availableQuantity >= 0 && group.availableQuantity <= group.requiredQuantity).reduce((totals, group) => {
                const fir = group.fir === true;
                let total = totals.find((entry) => entry.fir === fir);
                if (!total) {
                    total = { fir, availableQuantity: 0, requiredQuantity: 0, allocationUnknown: false, sources: [] };
                    totals.push(total);
                }
                const available = total.availableQuantity + group.availableQuantity;
                const required = total.requiredQuantity + group.requiredQuantity;
                total.availableQuantity = Number.isSafeInteger(available) ? available : NaN;
                total.requiredQuantity = Number.isSafeInteger(required) ? required : NaN;
                total.allocationUnknown = total.allocationUnknown || group.allocationUnknown === true;
                const sources = Array.isArray(group.sources) ? group.sources.filter((source) =>
                    source && typeof source === 'object' && !Array.isArray(source) &&
                    Number.isSafeInteger(source.requiredQuantity) && source.requiredQuantity > 0 &&
                    Number.isSafeInteger(source.availableQuantity) && source.availableQuantity >= 0 &&
                    source.availableQuantity <= source.requiredQuantity) : [];
                total.sources.push(...sources.length ? sources : [{
                    availableQuantity: group.availableQuantity, requiredQuantity: group.requiredQuantity,
                }]);
                return totals;
            }, []).sort((a, b) =>
                a.fir === true ? b.fir === true ? 0 : -1 : b.fir === true ? 1 : 0) : [];
        const noActiveRequirement = noRequiredQuantity && !visibleRequirements.some((requirement) => requirement.requiredQuantity > 0) && !anyGroups.length;
        const hasStock = tracked && Number.isSafeInteger(tracking.availableQuantity) && tracking.availableQuantity > 0;
        const nextItem = currentNextItem(tracking, multipleVariants);
        panel.classList.toggle('hidden', !nextItem && (noActiveRequirement && !hasStock && !error && !loading && !changing ||
            !tracked && !error && !loading && !changing));
        element('content').classList.toggle('hidden', !nextItem && (!tracked || noActiveRequirement && !hasStock));
        const variantName = multipleVariants ? variantLabel : '';
        element('variant-name').textContent = variantName;
        element('variant-name').classList.toggle('hidden', !variantName);
        panel.setAttribute('aria-label', variantName ? 'Items Tracker: ' + variantName : 'Items Tracker');
        // A backend a helyben mentett FiR/Regular készletből számolja a sorokat.
        const collectionCovered = noRequiredQuantity && (anyGroups.length > 0 || hasStock);
        const stockRequiredQuantity = collectionCovered ? tracking.availableQuantity : tracking && tracking.requiredQuantity;
        const stockRatio = tracked ? quantityText(tracking.availableQuantity) + '/' + quantityText(stockRequiredQuantity) : '—/—';
        element('ratio').textContent = stockRatio;
        trackingDisplayBaselines.set(panel, {stockRatio, hasRequirements: !!visibleRequirements.length || !!anyGroups.length});
        element('ratio').setAttribute('aria-label', 'Available / required quantity');
        element('requirements').replaceChildren(...visibleRequirements.map((requirement) => {
            const row = document.createElement('div');
            row.className = 'tracked-requirement-row';
            row.dataset.kind = requirement.kind;
            const label = document.createElement('dt');
            label.textContent = requirement.kind === 'fir' ? 'Found in Raid' : 'Regular';
            const quantity = document.createElement('dd');
            quantity.textContent = (requirement.allocationUnknown === true ? '—' : quantityText(requirement.availableQuantity)) + '/' + quantityText(requirement.requiredQuantity);
            row.setAttribute('aria-label', label.textContent + ' ' + quantity.textContent);
            row.append(label, quantity);
            return row;
        }), ...anyGroups.map((group) => {
            const row = document.createElement('div');
            row.className = 'tracked-requirement-row';
            row.dataset.kind = 'any';
            const label = document.createElement('dt');
            label.textContent = group.fir === true ? 'Shared (FiR)' : 'Shared';
            const quantity = document.createElement('dd');
            quantity.textContent = (group.allocationUnknown ? '—' : quantityText(group.availableQuantity)) + '/' + quantityText(group.requiredQuantity);
            row.setAttribute('aria-label', label.textContent + ' ' + quantity.textContent);
            row.append(label, quantity);
            return row;
        }));
        element('requirements').classList.toggle('hidden', !visibleRequirements.length && !anyGroups.length);
        stockActions.forEach((action) => {
            const button = element(action.id);
            button.disabled = !canChangeTracking(tracking, action.id);
            button.title = multipleVariants ? 'Tarkov Market API cannot update same-ID variants separately.' :
                action.label + (action.removing ? ' from' : ' to') + ' Tracked Items stock for the selected character';
        });
        element('status').textContent = status;
        element('status').classList.toggle('hidden', !status);
        element('error').textContent = error;
        element('error').classList.toggle('hidden', !error);
        renderNextItem(panel, nextItem);
    };
    const renderTracking = () => {
        nextItemContextKey();
        const entries = trackingEntries();
        if (!entries.length) nextItemCache = null;
        const panels = $('tracked-panels');
        const keys = new Set();
        const names = entries.map((tracking) => safeText(tracking.variantName).trim());
        entries.forEach((tracking, index) => {
            const key = JSON.stringify([tracking.itemUid, tracking.itemId || '', tracking.charUid, String(state.mode || '').toUpperCase(), tracking.variantName || '', index]);
            keys.add(key);
            let panel = trackingCards.get(key);
            if (!panel) {
                panel = $('tracked-panel-template').content.firstElementChild.cloneNode(true);
                stockActions.forEach((action) => panel.querySelector('[data-tracked="' + action.id + '"]').addEventListener('click', () => changeTrackedItem(action.id)));
                trackingCards.set(key, panel);
            }
            const name = names[index];
            const duplicateName = name && names.filter((candidate) => candidate.toLowerCase() === name.toLowerCase()).length > 1;
            const variantLabel = !name ? 'Variant ' + (index + 1) :
                duplicateName ? name + ' · Variant ' + (index + 1) : name;
            renderTrackingCard(panel, tracking, entries.length > 1, variantLabel);
            if (panels.children[index] !== panel) panels.insertBefore(panel, panels.children[index] || null);
        });
        for (const [key, panel] of trackingCards) {
            if (!keys.has(key)) {
                panel.remove();
                trackingCards.delete(key);
            }
        }
        panels.classList.toggle('hidden', !entries.length || entries.every((_, index) => panels.children[index].classList.contains('hidden')));
    };

    const showFeedback = (message, error = false) => {
        $('feedback').textContent = message;
        $('feedback').classList.toggle('hidden', !message);
        $('feedback').classList.toggle('error', error);
    };

    const renderLocalProgressStatus = () => {
        $('local-progress-status').classList.remove('hidden');
        const local = state.local;
        const error = safeText(local && local.error);
        const message = local && local.loading ? 'Refreshing Market progress…' : error ||
            safeText(local && local.warning) || (local && local.updatedAt ? 'Last sync: ' + localDateText(local.updatedAt) :
                'Select a character and refresh progress.');
        $('local-progress-status').textContent = message;
        $('local-progress-status').classList.toggle('error', !!error);
    };

    const updateKeyVisibility = () => {
        $('api-key').type = keyVisible ? 'text' : 'password';
        const label = keyRevealPending ? 'Cancel showing API key' :
            keyVisible ? 'Hide API key' : 'Show API key';
        $('btn-toggle-key').setAttribute('aria-label', label);
        $('btn-toggle-key').title = label;
        $('btn-toggle-key').setAttribute('aria-pressed', String(keyVisible));
        $('key-eye-slash').classList.toggle('hidden', !keyVisible);
    };
    const resetKeyDisplay = (clear = false) => {
        ++keyRevealRevision;
        keyRevealPending = false;
        keyVisible = false;
        if (clear) {
            $('api-key').value = '';
            keyFromStorage = false;
        }
        updateKeyVisibility();
    };

    const updateInventoryRowControls = (row, blocked = inventoryEditingBlocked()) => {
        const readOnly = row.dataset.readOnly === 'true';
        row.querySelectorAll('input').forEach((input) => { input.disabled = blocked || readOnly; });
        const button = row.querySelector('.inventory-save');
        if (button) button.disabled = blocked || readOnly || row.dataset.valid !== 'true' || row.dataset.changed !== 'true';
    };
    const updateControls = () => {
        const available = !!nativeApp() || preview;
        const busy = isBusy();
        $('search-name').disabled = !available;
        $('btn-search').disabled = !available || busy || !state.hasAPIKey || !$('search-name').value.trim();
        $('btn-scan').disabled = !available || busy || !state.hasAPIKey || !englishModelAvailable();
        $('btn-save-key').disabled = !available || busy || keyFromStorage || !$('api-key').value.trim();
        $('btn-clear-key').disabled = !available || busy || !state.hasAPIKey;
        $('btn-toggle-key').disabled = !available || busy || !state.hasAPIKey && !$('api-key').value;
        $('api-key').disabled = !available || busy;
        updateKeyVisibility();
        $('btn-item-page').disabled = !available || busy || !state.item || !state.item.uid;
        $('scan-automatic').disabled = !available || busy;
        $('btn-refresh-local').disabled = !available || busy || !state.hasAPIKey || !state.characterUid || !!(state.local && state.local.loading);
        const hotkeys = shortcutFields.map((field) => {
            const value = normalizedHotkey(state[field.property], field.fallback);
            const error = shortcutErrors.get(field.id) || safeText(state[field.error]);
            const pending = pendingOptions.has(field.id);
            const capturing = shortcutCaptureInput === $(field.id);
            const ready = value !== 'off' && state[field.ready] === true && !error && !pending && !state.hotkeyCapture;
            $(field.id).disabled = !available || busy;
            $('btn-disable-' + field.id).disabled = !available || busy || value === 'off' || pending;
            $(field.id + '-status').textContent = error || (pending ? 'Applying shortcut…' : capturing ?
                shortcutCapturePending ? 'Preparing shortcut capture…' : 'Press a shortcut.' : value === 'off' ?
                    'Shortcut disabled.' : ready ? value + ' is active globally.' : 'Shortcut is not active.');
            $(field.id + '-status').classList.toggle('error', !!error);
            return { value, ready };
        });
        $('game-mode').disabled = !available || busy;
        $('tracked-character').disabled = !available || busy || !state.hasAPIKey || !!state.charactersLoading ||
            !availableCharacters().length;
        const entries = trackingEntries();
        [...$('tracked-panels').children].forEach((panel, index) => {
            stockActions.forEach((action, actionIndex) => {
                panel.querySelector('[data-tracked="' + action.id + '"]').disabled = !canChangeTracking(entries[index], action.id);
                const badge = panel.querySelector('[data-tracked="' + action.id + '-hotkey"]');
                badge.textContent = hotkeys[actionIndex].value;
                badge.classList.toggle('hidden', !hotkeys[actionIndex].ready);
            });
            updateNextItem(panel, entries[index], entries.length > 1);
        });
        $('btn-min').disabled = !window.runtime || !window.runtime.WindowMinimise;
        $('btn-close').disabled = !nativeApp();
        $('btn-scan').querySelector('span').textContent = busy ? 'Processing…' : 'Scan now';
        $('inventory-search').disabled = !available || busy || inventoryLoading || !!inventoryRefreshRequest || !!(state.local && state.local.loading);
        $('btn-refresh-inventory').disabled = !available || busy || inventoryLoading || !!inventoryRefreshRequest || !!(state.local && state.local.loading) || !state.hasAPIKey || !state.characterUid;
        $('btn-import-inventory').disabled = !canTransferInventory();
        $('btn-export-inventory').disabled = !canTransferInventory();
        const inventoryBlocked = inventoryEditingBlocked();
        const controlsKey = [inventoryBlocked, inventoryRowsRevision].join(':');
        if (inventoryControlsKey !== controlsKey) {
            inventoryControlsKey = controlsKey;
            [...$('inventory-rows').children].forEach((row) => updateInventoryRowControls(row, inventoryBlocked));
        }
    };

    const currentInventoryContext = () => state.hasAPIKey && state.characterUid ?
        JSON.stringify([state.characterUid, String(state.mode || '').toUpperCase()]) : '';
    const currentInventoryRevision = () => safeText(state.local && state.local.inventoryRevision);
    const currentInventoryStamp = () => safeText(state.local && state.local.updatedAt);
    const inventoryPending = () => !!(state.pendingAdds && Number.isSafeInteger(state.pendingAdds.count) && state.pendingAdds.count > 0);
    const inventoryEditingBlocked = () => isBusy() || inventoryLoading || inventoryNeedsReload || !!inventoryRefreshRequest || !state.local || state.local.loading ||
        !!safeText(state.local.error) || !!safeText(state.error) || inventoryPending() || !inventoryData || inventoryContext !== currentInventoryContext() ||
        !!currentInventoryRevision() && inventoryData.revision !== currentInventoryRevision() ||
        !!currentInventoryStamp() && inventoryLoadedStamp !== currentInventoryStamp();
    const canTransferInventory = () => !!nativeApp() && !isBusy() && !inventoryLoading && !inventorySaving && !inventoryBackupBusy &&
        !inventoryRefreshRequest && !inventoryNeedsReload &&
        !!state.local && !state.local.loading && !safeText(state.local.error) && !safeText(state.error) &&
        !!inventoryData && !!inventoryData.profile && !!currentInventoryContext() && currentInventoryContext() === inventoryContext &&
        (!currentInventoryRevision() || inventoryData.revision === currentInventoryRevision()) &&
        (!currentInventoryStamp() || inventoryLoadedStamp === currentInventoryStamp()) &&
        !(state.pendingAdds && Number.isSafeInteger(state.pendingAdds.count) && state.pendingAdds.count > 0);
    const transferInventory = async (importing) => {
        if (!canTransferInventory()) return;
        const context = currentInventoryContext();
        const previousStatus = $('inventory-status').textContent;
        inventoryBackupBusy = true;
        localBusy = true;
        $('inventory-error').textContent = '';
        $('inventory-error').classList.add('hidden');
        $('inventory-status').textContent = importing ? 'Importing item classifications…' : 'Exporting item classifications…';
        updateControls();
        try {
            const result = await call(importing ? 'ImportTrackerQuality' : 'ExportTrackerQuality');
            if (context !== currentInventoryContext()) return;
            if (!result || typeof result !== 'object') throw new Error('Invalid backup result');
            if (result.canceled === true) {
                $('inventory-status').textContent = previousStatus;
                return;
            }
            if (safeText(result.error)) {
                $('inventory-status').textContent = '';
                $('inventory-error').textContent = safeText(result.error);
                $('inventory-error').classList.remove('hidden');
                return;
            }
            const counts = importing ? [result.imported, result.skipped] : [result.exported];
            if (!counts.every((value) => Number.isSafeInteger(value) && value >= 0)) throw new Error('Invalid backup counts');
            if (importing) {
                await refresh(await call('GetState'));
                if (context !== currentInventoryContext()) return;
                await loadInventory();
                $('inventory-status').textContent = 'Imported ' + quantityText(result.imported) + ' item classifications; skipped ' + quantityText(result.skipped) + ' unavailable items.';
            } else {
                $('inventory-status').textContent = 'Exported ' + quantityText(result.exported) + ' item classifications.';
            }
        } catch (_) {
            if (context === currentInventoryContext()) {
                $('inventory-status').textContent = '';
                $('inventory-error').textContent = importing ? 'Item classifications could not be imported. Please try again.' : 'Item classifications could not be exported. Please try again.';
                $('inventory-error').classList.remove('hidden');
            }
        } finally {
            inventoryBackupBusy = false;
            localBusy = false;
            updateControls();
            synchronizeInventory();
        }
    };
    const resetInventory = () => {
        ++inventoryRevision;
        inventoryData = null;
        inventoryLoading = false;
        inventorySyncAttempt = '';
        inventoryRefreshRequest = null;
        inventorySyncErrorShown = false;
        inventoryNeedsReload = false;
        inventoryLoadedStamp = '';
        $('inventory-rows').replaceChildren();
        ++inventoryRowsRevision;
        $('inventory-status').textContent = state.hasAPIKey && state.characterUid ? 'Open Items Tracker to classify stock.' : 'Select a character to view stock.';
        $('inventory-error').textContent = '';
        $('inventory-error').classList.add('hidden');
    };
    const renderInventory = () => {
        const query = $('inventory-search').value.trim().toLowerCase();
        const allRows = inventoryData && Array.isArray(inventoryData.rows) ? inventoryData.rows.filter((row) => row &&
            safeText(row.itemId) && Number.isSafeInteger(row.total) && row.total > 0 && Number.isSafeInteger(row.fir) && row.fir >= 0 &&
            Number.isSafeInteger(row.regular) && row.regular >= 0 && row.fir + row.regular <= row.total).sort((left, right) => {
                const uncategorizedOrder = Number(right.total > right.fir + right.regular) - Number(left.total > left.fir + left.regular);
                const nameOrder = inventoryNameCollator.compare(safeText(left.name) || left.itemId, safeText(right.name) || right.itemId);
                const leftKey = safeText(left.rowKey) || left.itemId;
                const rightKey = safeText(right.rowKey) || right.itemId;
                return uncategorizedOrder || nameOrder || (leftKey < rightKey ? -1 : leftKey > rightKey ? 1 : 0);
            }) : [];
        const rows = allRows.filter((row) => (safeText(row.name) || row.itemId).toLowerCase().includes(query));
        $('inventory-rows').replaceChildren(...rows.map((item) => {
            const row = document.createElement('div');
            row.className = 'inventory-row';
            row.dataset.itemId = item.itemId;
            row.dataset.rowKey = item.rowKey || item.itemId;
            row.dataset.readOnly = String(item.readOnly === true);
            const heading = document.createElement('div');
            heading.className = 'inventory-item-heading';
            const imageWrap = document.createElement('div');
            imageWrap.className = 'inventory-item-image';
            const placeholder = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
            placeholder.classList.add('item-image-placeholder');
            placeholder.setAttribute('viewBox', '0 0 48 48');
            placeholder.setAttribute('aria-hidden', 'true');
            const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
            path.setAttribute('d', 'm24 5 17 10v18L24 43 7 33V15Z M7 15l17 10 17-10M24 25v18M16 10l17 10');
            placeholder.append(path);
            imageWrap.append(placeholder);
            const imageURL = safeImageURL(item.icon);
            if (imageURL) {
                const image = document.createElement('img');
                image.className = 'inventory-image-pending';
                image.alt = '';
                image.referrerPolicy = 'no-referrer';
                image.loading = 'lazy';
                image.addEventListener('load', () => {
                    image.classList.remove('inventory-image-pending', 'hidden');
                    placeholder.classList.add('hidden');
                });
                image.addEventListener('error', () => {
                    image.classList.add('hidden');
                    image.classList.remove('inventory-image-pending');
                    placeholder.classList.remove('hidden');
                });
                image.src = imageURL;
                imageWrap.append(image);
            }
            const name = document.createElement('p');
            name.className = 'inventory-name';
            name.textContent = safeText(item.name) || item.itemId;
            name.append(document.createTextNode(' '), copyNameButton(name.textContent, 'item'));
            heading.append(imageWrap, name);
            const quantities = document.createElement('div');
            quantities.className = 'inventory-quantities';
            const total = document.createElement('span');
            total.textContent = 'API total: ' + quantityText(item.total);
            const uncategorized = document.createElement('span');
            quantities.append(total, uncategorized);
            const fields = document.createElement('div');
            fields.className = 'inventory-quality-fields';
            const inputs = ['fir', 'regular'].map((kind) => {
                const label = document.createElement('label');
                label.className = 'inventory-quality-field';
                label.textContent = kind === 'fir' ? 'Found in Raid' : 'Regular';
                const input = document.createElement('input');
                input.type = 'number';
                input.min = '0';
                input.max = String(item.total);
                input.step = '1';
                input.value = String(item[kind]);
                input.setAttribute('aria-label', (safeText(item.name) || item.itemId) + ': ' + label.textContent);
                label.append(input);
                fields.append(label);
                return input;
            });
            const save = document.createElement('button');
            save.type = 'button';
            save.className = 'inventory-save';
            save.textContent = 'Save classification locally';
            const error = document.createElement('p');
            error.className = 'inventory-row-error hidden';
            error.setAttribute('role', 'alert');
            const updateRow = () => {
                const values = inputs.map((input) => input.value.trim() === '' ? NaN : Number(input.value));
                const valid = values.every((value) => Number.isSafeInteger(value) && value >= 0 && value <= item.total) && values[0] + values[1] <= item.total;
                row.dataset.valid = String(valid);
                row.dataset.changed = String(values[0] !== item.fir || values[1] !== item.regular);
                uncategorized.textContent = 'Uncategorized: ' + (valid ? quantityText(item.total - values[0] - values[1]) : '—');
                error.textContent = valid ? item.readOnly ? 'This stock is read-only.' : '' : 'FiR + Regular must be whole numbers between 0 and the API total.';
                error.classList.toggle('hidden', !error.textContent);
                updateInventoryRowControls(row);
            };
            inputs.forEach((input) => input.addEventListener('input', updateRow));
            save.addEventListener('click', async () => {
                if (inventorySaving || inventoryEditingBlocked() || !inventoryData || row.dataset.valid !== 'true' || row.dataset.changed !== 'true') return;
                const context = currentInventoryContext();
                if (!context || context !== inventoryContext || item.readOnly) return;
                inventorySaving = true;
                localBusy = true;
                $('inventory-status').textContent = 'Saving classification locally…';
                updateControls();
                try {
                    const result = await call('SetTrackerItemQuality', inventoryData.profile, item.rowKey || item.itemId, item.total, Number(inputs[0].value), Number(inputs[1].value));
                    if (context === currentInventoryContext()) {
                        await refresh(result);
                        if (safeText(result && result.local && result.local.error) || safeText(result && result.error)) throw new Error('Classification rejected');
                        await loadInventory();
                        $('inventory-status').textContent = 'Classification saved locally. Tarkov Market total unchanged.';
                    }
                } catch (_) {
                    if (context === currentInventoryContext()) {
                        $('inventory-status').textContent = '';
                        error.textContent = 'Classification could not be saved. Refresh inventory and try again.';
                        error.classList.remove('hidden');
                    }
                } finally {
                    inventorySaving = false;
                    localBusy = false;
                    updateControls();
                    synchronizeInventory();
                }
            });
            row.append(heading, quantities, fields, save, error);
            // A kezdeti validáció a már beillesztett soron frissíti a vezérlőket.
            const values = inputs.map((input) => Number(input.value));
            row.dataset.valid = 'true';
            row.dataset.changed = 'false';
            uncategorized.textContent = 'Uncategorized: ' + quantityText(item.total - values[0] - values[1]);
            if (item.readOnly) {
                error.textContent = 'This stock is read-only.';
                error.classList.remove('hidden');
            }
            return row;
        }));
        ++inventoryRowsRevision;
        $('inventory-status').textContent = rows.length ? quantityText(rows.length) + ' of ' + quantityText(allRows.length) + ' stocked items.' :
            query ? 'No stocked items match your search.' : 'No items with stock for this character.';
        updateControls();
    };
    const loadInventory = async () => {
        const context = currentInventoryContext();
        if (!context || inventoryLoading || !state.local || state.local.loading || inventoryPending()) return;
        const revision = ++inventoryRevision;
        const stamp = currentInventoryStamp();
        inventoryContext = context;
        inventorySyncAttempt = JSON.stringify([context, currentInventoryRevision(), stamp]);
        inventoryLoading = true;
        $('inventory-status').textContent = 'Loading inventory…';
        $('inventory-error').textContent = '';
        $('inventory-error').classList.add('hidden');
        updateControls();
        try {
            const result = await call('GetTrackerInventory');
            if (revision !== inventoryRevision || context !== currentInventoryContext()) return;
            if (!state.local || state.local.loading || safeText(state.local.error) || safeText(state.error) || inventoryPending()) return;
            if (currentInventoryStamp() !== stamp) return;
            if (!result || !result.profile || !Array.isArray(result.rows) || safeText(result.error)) throw new Error('Inventory unavailable');
            if (currentInventoryRevision() && result.revision !== currentInventoryRevision()) {
                inventorySyncErrorShown = true;
                inventoryNeedsReload = true;
                $('inventory-error').textContent = 'Inventory changed during loading. Refresh inventory before editing classifications.';
                $('inventory-error').classList.remove('hidden');
                return;
            }
            inventoryData = result;
            inventoryLoadedStamp = stamp;
            inventorySyncErrorShown = false;
            inventoryNeedsReload = false;
            renderInventory();
        } catch (_) {
            if (revision === inventoryRevision && context === currentInventoryContext()) {
                inventoryNeedsReload = true;
                $('inventory-status').textContent = '';
                $('inventory-error').textContent = 'Inventory could not be loaded. Refresh inventory and try again.';
                $('inventory-error').classList.remove('hidden');
            }
        } finally {
            if (revision === inventoryRevision) inventoryLoading = false;
            updateControls();
            synchronizeInventory();
        }
    };

    const synchronizeInventory = () => {
        const context = currentInventoryContext();
        if (!$('tracker-inventory').open || !context || context !== inventoryContext) return;
        if (state.local && state.local.loading) return;
        const error = safeText(state.local && state.local.error) || safeText(state.error);
        if (error) {
            if (!inventoryNeedsReload) inventorySyncAttempt = '';
            inventoryNeedsReload = true;
            if (inventoryRefreshRequest && !inventoryRefreshRequest.awaitingResponse) inventoryRefreshRequest = null;
            inventorySyncErrorShown = true;
            $('inventory-error').textContent = 'Inventory may be outdated. ' + error + ' Refresh inventory before editing classifications.';
            $('inventory-error').classList.remove('hidden');
            updateControls();
            return;
        }
        if (!state.local || isBusy() || inventoryLoading || inventorySaving || inventoryBackupBusy || inventoryPending() ||
            inventoryRefreshRequest && inventoryRefreshRequest.awaitingResponse) return;
        const forced = !!inventoryRefreshRequest;
        const expected = currentInventoryRevision();
        const stamp = currentInventoryStamp();
        const attempt = JSON.stringify([context, expected, stamp]);
        const stale = inventoryNeedsReload || !inventoryData || !!expected && inventoryData.revision !== expected || !!stamp && inventoryLoadedStamp !== stamp;
        if (inventorySyncErrorShown && !stale) {
            inventorySyncErrorShown = false;
            $('inventory-error').textContent = '';
            $('inventory-error').classList.add('hidden');
        }
        if (!forced && (!stale || inventorySyncAttempt === attempt)) return;
        inventoryRefreshRequest = null;
        if (inventorySyncErrorShown) {
            inventorySyncErrorShown = false;
            $('inventory-error').textContent = '';
            $('inventory-error').classList.add('hidden');
        }
        loadInventory();
    };

    const refreshInventoryFromAPI = async () => {
        const context = currentInventoryContext();
        if ((!nativeApp() && !preview) || !context || isBusy() || inventoryLoading || inventorySaving || inventoryBackupBusy ||
            inventoryRefreshRequest || state.local && state.local.loading) return;
        const request = { context, awaitingResponse: true };
        inventoryRefreshRequest = request;
        $('inventory-status').textContent = 'Refreshing inventory from Tarkov Market…';
        updateControls();
        try {
            const result = await call('RefreshLocalProgress');
            if (inventoryRefreshRequest !== request || context !== currentInventoryContext()) return;
            await refresh(result);
            if (inventoryRefreshRequest !== request || context !== currentInventoryContext()) return;
            request.awaitingResponse = false;
            synchronizeInventory();
        } catch (_) {
            if (inventoryRefreshRequest === request && context === currentInventoryContext()) {
                inventoryRefreshRequest = null;
                inventoryNeedsReload = true;
                $('inventory-status').textContent = '';
                $('inventory-error').textContent = 'Inventory could not be refreshed from Tarkov Market. Please try again.';
                $('inventory-error').classList.remove('hidden');
            }
        } finally {
            updateControls();
        }
    };

    const updateImage = (item) => {
        const source = item ? safeImageURL(item.img) || safeImageURL(item.icon) : '';
        const image = $('item-image');
        if (source) {
            if (image.getAttribute('src') !== source) {
                image.classList.add('hidden');
                $('item-image-placeholder').classList.remove('hidden');
                image.src = source;
            }
            image.alt = safeText(item.name) || 'Item image';
        } else {
            image.removeAttribute('src');
            image.alt = '';
            image.classList.add('hidden');
            $('item-image-placeholder').classList.remove('hidden');
        }
    };

    const render = (nextState) => {
        if (!nextState || typeof nextState !== 'object') return;
        const sequence = nextState.stateSequence;
        if (Number.isSafeInteger(sequence) && sequence >= 0) {
            if (latestStateSequence !== null && sequence < latestStateSequence) return;
            latestStateSequence = sequence;
        } else if (latestStateSequence !== null || sequence !== undefined) {
            return;
        }
        state = { ...state, ...nextState, pendingAdds: nextState.pendingAdds && typeof nextState.pendingAdds === 'object' ? nextState.pendingAdds : null };
        const stockTracking = currentTracking();
        const stockContext = stockTrackingContext(stockTracking);
        if (stockContext !== loadedStockContext) loadedStockContext = '';
        if (stockContext && state.local && !state.local.loading && !stockTracking.loading && !stockTracking.adding &&
            !safeText(state.error) && !safeText(state.local.error) && !safeText(stockTracking.error)) loadedStockContext = stockContext;
        const nextInventoryContext = currentInventoryContext();
        if (inventoryContext !== nextInventoryContext) {
            inventoryContext = nextInventoryContext;
            resetInventory();
        }
        if (!state.hasAPIKey && (keyFromStorage || keyRevealPending)) resetKeyDisplay(true);
        const trackedState = currentTracking();
        if (trackingFailure && (!sameTrackingTarget(trackingFailure) || trackedState && (trackedState.loading || trackedState.adding))) {
            trackingFailure = null;
        }
        const item = state.item;
        const marketAvailable = !!item && item.haveMarketData === true && !item.bannedOnFlea;
        const slots = item && Number.isInteger(item.slots) && item.slots > 0 ? item.slots : 0;
        const itemName = item ? safeText(item.name) || safeText(item.shortName) : '';
        $('item-name-text').textContent = itemName || (item ? 'Unknown item' : 'Waiting for item');
        $('btn-copy-item').classList.toggle('hidden', !itemName);
        $('item-meta').textContent = item ? (slots ? numberText(slots) + (slots === 1 ? ' slot' : ' slots') : 'Slots: —') :
            'Open the item Inspect window in the game.';
        renderPrice('current-price', 'current-slot-price', marketAvailable ? item.price : null, slots);
        renderPrice('avg-price', 'slot-price', marketAvailable ? item.avg24hPrice : null, slots);
        $('trader-name').textContent = item ? safeText(item.traderName) || '—' : '—';
        renderPrice('trader-price', 'trader-slot-price', item ? item.traderPrice : null, slots, item ? item.traderPriceCur : '₽');
        $('market-note').textContent = !item ? '' : item.bannedOnFlea ? 'This item cannot be sold on the Flea Market.' :
            !marketAvailable ? 'No Flea Market price data is available for this item.' : '';
        $('market-note').classList.toggle('hidden', !$('market-note').textContent);
        updateImage(item);

        const active = document.activeElement;
        const updateOption = (id, property, value) => {
            if (!pendingOptions.has(id) && active !== $(id)) $(id)[property] = value;
        };
        updateOption('scan-automatic', 'checked', !!state.enabled && !!state.automatic);
        shortcutFields.forEach((field) => updateOption(field.id, 'value', shortcutDisplay(normalizedHotkey(state[field.property], field.fallback))));
        updateOption('game-mode', 'value', String(state.mode || 'PVP').toUpperCase());
        renderCharacters();
        updateOption('tracked-character', 'value', safeText(state.characterUid));
        renderTracking();
        renderLocalProgressStatus();

        $('key-indicator').textContent = state.hasAPIKey ? 'API key saved' : 'API key required';
        $('key-indicator').classList.toggle('saved', !!state.hasAPIKey);
        $('api-key').placeholder = state.hasAPIKey ? '••••••••••••••••' : 'Enter API key';
        $('mode-badge').textContent = String(state.mode || 'PVP').toUpperCase();
        const status = safeText(state.status);
        const automaticEnabled = !!state.enabled && !!state.automatic;
        const waitingStatus = ['', 'idle', 'ready', 'waiting', 'disabled', 'Waiting for an item Inspect window.',
            'Waiting for the game', 'Automatic scanning is off.'].includes(status);
        const pendingCount = state.pendingAdds && Number.isSafeInteger(state.pendingAdds.count) && state.pendingAdds.count > 0 ? state.pendingAdds.count : 0;
        const pendingError = safeText(state.pendingAdds && state.pendingAdds.error);
        const pendingStatus = safeText(state.pendingAdds && state.pendingAdds.status);
        const stalledPending = pendingCount > 0 && state.pendingAdds.running !== true;
        const scanStatus = stalledPending && (pendingError || pendingStatus) ? pendingError || pendingStatus : state.hasAPIKey && !automaticEnabled && waitingStatus ? statusNames.disabled :
                statusNames[status] || status || (state.hasAPIKey ? 'Waiting for an item Inspect window.' : 'API key required');
        $('status-text').textContent = scanStatus + (pendingCount > 0 ? ' (' + itemsText(pendingCount) + ' waiting for API save)' : '');
        $('status-dot').className = 'status-dot' + (state.error || pendingError ? ' error' :
            isBusy() || pendingCount > 0 ? ' busy' : state.hasAPIKey && automaticEnabled ? ' ready' : '');
        $('error-text').textContent = safeText(state.error) || (stalledPending ? '' : pendingError);
        $('error-text').classList.toggle('hidden', !$('error-text').textContent);

        const details = state.scanDetails && typeof state.scanDetails === 'object' ? state.scanDetails : null;
        $('scan-details').classList.toggle('hidden', !details);
        if (details) {
            const count = Number.isInteger(details.inspectCount) && details.inspectCount >= 0 ? details.inspectCount : 0;
            $('inspect-count').textContent = numberText(count);
            const titles = Array.isArray(details.titles) ? details.titles.map(safeText).filter(Boolean) : [];
            $('scan-titles').replaceChildren(...titles.map((title) => {
                const entry = document.createElement('li');
                entry.textContent = title;
                return entry;
            }));
            $('scan-titles').classList.toggle('hidden', !titles.length);
            $('scan-titles-empty').classList.toggle('hidden', !!titles.length);
            $('scan-outcome').textContent = safeText(details.outcome) || '—';
        } else {
            $('scan-details').open = false;
            $('inspect-count').textContent = '0';
            $('scan-titles').replaceChildren();
            $('scan-outcome').textContent = '';
        }

        const dataNote = localDateText(state.cachedAt);
        $('data-age').textContent = dataNote;
        $('data-note').classList.toggle('hidden', !dataNote);
        $('data-note').classList.toggle('stale', !!state.stale);
        $('data-note').title = state.stale ? 'Cached data; refresh required.' : '';
        if (initialRender) {
            $('settings').open = !state.hasAPIKey;
            initialRender = false;
        }
        updateControls();
        synchronizeInventory();
    };

    const previewVariants = preview && new URLSearchParams(window.location.search).get('variants') === '1';
    const previewCompletion = preview && new URLSearchParams(window.location.search).get('completion') === '1';
    const sampleItem = previewVariants ? {
        uid: 'preview-mf-untar', bsgId: 'preview-tracked-item', name: 'MF-UNTAR body armor', shortName: 'MF-UN',
        price: 54999, avg24hPrice: 68710, traderName: 'Ragman', traderPrice: 13112, traderPriceCur: '₽', slots: 9,
        haveMarketData: true, bannedOnFlea: false,
    } : {
        uid: 'preview-item', bsgId: 'preview-tracked-item', name: 'Galvion Caiman Hybrid helmet (MultiCam Alpine)', shortName: 'Caiman',
        price: 162000, avg24hPrice: 154800, traderName: 'Ragman', traderPrice: 25660, traderPriceCur: '₽', slots: 4,
        haveMarketData: true, bannedOnFlea: false,
    };
    let previewState = {
        ...state, ...Object.fromEntries(shortcutFields.flatMap((field) => [[field.property, field.fallback], [field.ready, true]])),
        hasAPIKey: true, status: 'Item recognized.', ocrLanguages: ['en'], cachedAt: '2026-10-10T22:30:23Z', item: sampleItem,
        characterUid: 'preview-character', characters: [{ charUid: 'preview-character', name: 'Sample character', gameMode: 'PVP' }],
        tracking: { itemUid: sampleItem.uid, itemId: 'preview-tracked-item', charUid: 'preview-character',
            tracked: true, loading: false, adding: false, availableQuantity: 0, requiredQuantity: 7,
            remainingQuantity: 7, requirement: 'fir', requirements: [{ kind: 'fir', requiredQuantity: 7 }], error: '', status: '' },
    };
    if (previewVariants) {
        const variantError = 'Separate stock updates are unavailable for variants sharing an item ID in the current Tarkov Market API.';
        const regular = { ...previewState.tracking, rowKey: 'preview-variant-0', qualityKnown: true,
            variantName: 'MF-UNTAR body armor (0/50)', availableQuantity: 1, firQuantity: 0, regularQuantity: 0, uncategorizedQuantity: 1,
            icon: 'https://cdn.tarkov-market.app/images/items/mf-untar_armor_vest_(0_45)_sm.png',
            requiredQuantity: 1, remainingQuantity: 1, requirement: 'regular',
            requirements: [{ kind: 'regular', requiredQuantity: 1, availableQuantity: 0 }],
            addFIRAllowed: false, addRegularAllowed: false, removeFIRAllowed: false, removeRegularAllowed: false, error: variantError };
        const fir = { ...previewState.tracking, rowKey: 'preview-variant-1', qualityKnown: true,
            variantName: 'MF-UNTAR body armor', availableQuantity: 2, firQuantity: 0, regularQuantity: 0, uncategorizedQuantity: 2,
            icon: 'https://cdn.tarkov-market.app/images/items/689a1ed7-ae05-4d5d-b8eb-5e28e9276b4e_enImg.webp?r=1736022219428',
            requiredQuantity: 0, remainingQuantity: 0, requirement: 'unknown', requirements: [],
            anyGroups: [{ groupId: 'quest:scavenger:body-armor', fir: true, requiredQuantity: 15, availableQuantity: 6,
                sources: [{ questName: 'Scavenger', category: 'Body Armor', requiredQuantity: 15, availableQuantity: 6,
                    items: [{ itemId: 'preview-mf-untar', name: 'MF-UNTAR body armor', availableQuantity: 2 },
                        { itemId: 'preview-6b13', name: '6B13 assault armor (EMR)', availableQuantity: 3 },
                        { itemId: 'preview-interceptor', name: 'Interceptor OTV body armor (UCP)', availableQuantity: 1 }] }] }],
            addFIRAllowed: false, addRegularAllowed: false, removeFIRAllowed: false, removeRegularAllowed: false, error: variantError };
        previewState.tracking = { ...fir, variants: [regular, fir] };
    }
    if (preview && !previewVariants) {
        previewState.item = { ...sampleItem, name: 'Salewa first aid kit', shortName: 'Salewa', slots: 2,
            price: 0, avg24hPrice: 0, traderPrice: 0, traderName: '', haveMarketData: false };
        previewState.tracking = { ...previewState.tracking, requirement: 'mixed',
            availableQuantity: previewCompletion ? 0 : 2, requiredQuantity: previewCompletion ? 7 : 8, remainingQuantity: previewCompletion ? 7 : 6,
            firQuantity: previewCompletion ? 0 : 2, regularQuantity: 0, uncategorizedQuantity: 0,
            addFIRAllowed: true, addRegularAllowed: true, removeFIRAllowed: !previewCompletion, removeRegularAllowed: false,
            marketAvailableQuantity: previewCompletion ? 0 : 2,
            requirements: [{ kind: 'fir', requiredQuantity: previewCompletion ? 2 : 5, availableQuantity: previewCompletion ? 0 : 2 },
                { kind: 'regular', requiredQuantity: previewCompletion ? 5 : 3, availableQuantity: 0 }] };
    }
    const updatePreviewTracking = () => {
        if (!previewState.tracking) return;
        if (previewVariants) {
            const variants = previewState.tracking.variants.map((variant) => {
                const requirements = variant.requirements.map((requirement) => ({ ...requirement,
                    availableQuantity: Math.min(requirement.requiredQuantity, requirement.kind === 'fir' ? variant.firQuantity : variant.firQuantity + variant.regularQuantity) }));
                const anyGroups = (variant.anyGroups || []).map((group) => {
                    const availableQuantity = Math.min(group.requiredQuantity, group.fir ? variant.firQuantity : variant.firQuantity + variant.regularQuantity);
                    return { ...group, availableQuantity, sources: group.sources.map((source) => ({ ...source, availableQuantity,
                        items: source.items.map((item, index) => ({ ...item, availableQuantity: index === 0 ? availableQuantity : 0 })) })) };
                });
                const targets = [...requirements.map((row) => ({ kind: 'quest', goalName: 'Fishing Gear', availableQuantity: row.availableQuantity,
                    requiredQuantity: row.requiredQuantity })), ...anyGroups.map((group) => ({ kind: 'quest', goalName: 'Scavenger',
                    availableQuantity: group.availableQuantity, requiredQuantity: group.requiredQuantity }))];
                return { ...variant, requirements, anyGroups, nextItem: { status: 'estimated', targets } };
            });
            previewState.tracking = { ...variants[variants.length - 1], variants };
            return;
        }
        const tracking = previewState.tracking;
        let fir = tracking.firQuantity || 0;
        let regular = tracking.regularQuantity || 0;
        const requirements = tracking.requirements.map((requirement) => {
            const availableQuantity = Math.min(requirement.requiredQuantity, requirement.kind === 'fir' ? fir : regular + fir);
            if (requirement.kind === 'fir') fir -= availableQuantity;
            else {
                const usedRegular = Math.min(regular, availableQuantity);
                regular -= usedRegular;
                fir -= availableQuantity - usedRegular;
            }
            return { ...requirement, availableQuantity };
        });
        const firGap = requirements.some((row) => row.kind === 'fir' && row.availableQuantity < row.requiredQuantity);
        const regularGap = requirements.some((row) => row.kind === 'regular' && row.availableQuantity < row.requiredQuantity);
        const availableQuantity = tracking.firQuantity + tracking.regularQuantity + tracking.uncategorizedQuantity;
        const room = availableQuantity < tracking.requiredQuantity;
        previewState.tracking = { ...tracking, requirements, availableQuantity,
            remainingQuantity: Math.max(0, tracking.requiredQuantity - availableQuantity),
            addFIRAllowed: room && (firGap || regularGap), addRegularAllowed: room && regularGap,
            removeFIRAllowed: tracking.firQuantity > 0, removeRegularAllowed: tracking.regularQuantity > 0,
            nextItem: { status: 'estimated', targets: requirements.map((row) => ({ kind: 'quest',
                goalName: row.kind === 'fir' ? 'Shortage' : 'A Bitter Victory',
                availableQuantity: row.availableQuantity, requiredQuantity: row.requiredQuantity, completed: false })) } };
    };
    if (preview) updatePreviewTracking();
    if (preview) {
        previewState.pendingAdds = { count: 0, running: false, status: '', error: '' };
        previewState.local = { loading: false, error: '', warning: '', updatedAt: '2026-10-11T21:30:00Z' };
    }
    let previewAPIKey = 'preview-api-key';
    const call = async (method, ...args) => {
        const app = nativeApp();
        if (app && typeof app[method] === 'function') return app[method](...args);
        if (!preview) throw new Error('The app connection is unavailable.');
        if (method === 'RevealAPIKey') return previewState.hasAPIKey ? previewAPIKey : '';
        if (method === 'GetTrackerInventory') {
            const tracking = previewState.tracking;
            const entries = tracking ? tracking.variants || [tracking] : [];
            return { profile: 'preview-profile', revision: 1, error: '', rows: entries.map((entry) => ({ itemId: entry.itemId,
                rowKey: entry.rowKey || entry.itemId, name: entry.variantName || previewState.item.name, icon: entry.icon || '',
                total: entry.availableQuantity, fir: entry.firQuantity || 0, regular: entry.regularQuantity || 0,
                uncategorized: entry.uncategorizedQuantity || 0, readOnly: entry.firInventorySeparate === true })) };
        }
        if (method === 'SetTrackerItemQuality') {
            const [profile, rowKey, total, fir, regular] = args;
            const entries = previewState.tracking ? previewState.tracking.variants || [previewState.tracking] : [];
            const tracking = entries.find((entry) => (entry.rowKey || entry.itemId) === rowKey);
            if (profile !== 'preview-profile' || !tracking || tracking.firInventorySeparate === true || tracking.availableQuantity !== total ||
                ![fir, regular].every((value) => Number.isSafeInteger(value) && value >= 0) || fir + regular > total) throw new Error('Invalid classification');
            Object.assign(tracking, { firQuantity: fir, regularQuantity: regular, uncategorizedQuantity: total - fir - regular });
            updatePreviewTracking();
        }
        if (method === 'SetOptions') {
            const values = shortcutFields.map((field) => normalizedHotkey(args[0][field.property], field.fallback));
            if (new Set(values.filter((value) => value !== 'off')).size !== values.filter((value) => value !== 'off').length) throw new Error('Duplicate shortcut.');
            previewState = { ...previewState, ...args[0], ...Object.fromEntries(shortcutFields.flatMap((field, index) =>
                [[field.property, values[index]], [field.ready, values[index] !== 'off' && !previewState.hotkeyCapture], [field.error, '']])) };
        }
        if (method === 'SetHotkeyCapture') previewState = { ...previewState, hotkeyCapture: !!args[0],
            ...Object.fromEntries(shortcutFields.map((field) => [field.ready, !args[0] && previewState[field.property] !== 'off'])) };
        if (method === 'RefreshLocalProgress' && previewState.local) {
            if (!previewVariants) {
                const requiredQuantity = previewCompletion ? 4 : 5;
                const firRequired = previewCompletion ? 1 : 2;
                const requirements = [{ kind: 'fir', requiredQuantity: firRequired, availableQuantity: 0 },
                    { kind: 'regular', requiredQuantity: 3, availableQuantity: 0 }];
                previewState.tracking = { ...previewState.tracking, requiredQuantity,
                    remainingQuantity: Math.max(0, requiredQuantity - previewState.tracking.availableQuantity),
                    requirements,
                    completedSources: [{ type: 'quest', quantity: 3, optional: false }] };
                updatePreviewTracking();
            }
            previewState.local = { ...previewState.local, updatedAt: new Date().toISOString() };
        }
        if (method === 'SaveAPIKey') {
            previewAPIKey = args[0];
            previewState = { ...previewState, hasAPIKey: true, status: 'ready' };
        }
        if (method === 'ClearAPIKey') {
            previewAPIKey = '';
            previewState = { ...previewState, hasAPIKey: false, status: 'no_key', item: null, characterUid: '', characters: [], tracking: null };
        }
        if (method === 'SearchItem' || method === 'ScanNow') {
            const item = previewState.item || sampleItem;
            previewState = { ...previewState, item, status: 'Item recognized.' };
        }
        const stockAction = stockActions.find((action) => action.method === method);
        if (stockAction && previewState.tracking && previewState.tracking[stockAction.allowed] && !previewState.tracking.error &&
            !previewState.tracking.variants && args[0] === previewState.tracking.itemUid) {
            const tracking = previewState.tracking;
            previewState.tracking = { ...tracking, [stockAction.quantity]: tracking[stockAction.quantity] + (stockAction.removing ? -1 : 1),
                status: (stockAction.removing ? 'Removed' : 'Added') + ' 1 item. Saving to Tarkov Market…' };
            updatePreviewTracking();
            const count = (previewState.pendingAdds && previewState.pendingAdds.count || 0) + 1;
            previewState.pendingAdds = { count, running: true, status: 'Saving to Tarkov Market…', error: '' };
            setTimeout(() => {
                const count = Math.max(0, previewState.pendingAdds.count - 1);
                previewState.pendingAdds = { count, running: count > 0, status: count > 0 ? 'Saving to Tarkov Market…' : '', error: '' };
                if (!count && previewState.tracking) {
                    const stock = previewState.tracking.availableQuantity;
                    previewState.tracking = { ...previewState.tracking, marketAvailableQuantity: stock,
                        status: 'Saved on Tarkov Market.' };
                }
                render(previewState);
            }, 1800);
        }
        return { ...previewState };
    };
    const refresh = async (result) => {
        render(result && typeof result === 'object' ? result : await call('GetState'));
    };
    const action = async (method, args = [], success = '') => {
        if (isBusy()) return false;
        const context = progressContext();
        localBusy = true;
        if (method === 'SaveAPIKey' || method === 'ClearAPIKey') resetInventory();
        showFeedback('');
        updateControls();
        try {
            const result = await call(method, ...args);
            await refresh(result);
            if (method === 'RefreshLocalProgress' && context === progressContext() && trackingFailure &&
                sameTrackingTarget(trackingFailure) && currentTracking() && !safeText(currentTracking().error) &&
                state.local && !state.local.loading && !safeText(state.local.error) &&
                Number.isSafeInteger(result && result.stateSequence) && result.stateSequence === latestStateSequence &&
                Number.isSafeInteger(trackingFailure.stateSequence) && result.stateSequence > trackingFailure.stateSequence) {
                trackingFailure = null;
                renderTracking();
            }
            if (success && !state.error) showFeedback(success);
            return !state.error;
        } catch (_) {
            showFeedback('The action failed. Please try again.', true);
            return false;
        } finally {
            localBusy = false;
            updateControls();
            synchronizeInventory();
        }
    };

    const readOptions = () => ({
        enabled: true, automatic: $('scan-automatic').checked,
        ...Object.fromEntries(shortcutFields.map((field) => [field.property, normalizedHotkey($(field.id).value, field.fallback)])),
        language: 'en', mode: $('game-mode').value, characterUid: $('tracked-character').value,
    });
    const saveOptions = (event) => {
        if (isBusy()) return;
        pendingOptions.add(event.target.id);
        const character = availableCharacters().find((entry) => entry.charUid === $('tracked-character').value);
        if (event.target.id === 'tracked-character' && character) {
            const mode = characterMode(character);
            if (['PVP', 'PVE', 'SEASON'].includes(mode)) {
                $('game-mode').value = mode;
                pendingOptions.add('game-mode');
            }
        } else if (event.target.id === 'game-mode' && character && characterMode(character) !== $('game-mode').value) {
            $('tracked-character').value = '';
            pendingOptions.add('tracked-character');
        }
        ++optionsRevision;
        updateControls();
        optionsQueue = optionsQueue.then(async () => {
            const revision = optionsRevision;
            const options = readOptions();
            try {
                const result = await call('SetOptions', options);
                if (revision === optionsRevision) pendingOptions.clear();
                await refresh(result);
            } catch (_) {
                if (revision === optionsRevision) pendingOptions.clear();
                shortcutFields.forEach((field) => { $(field.id).value = shortcutDisplay(normalizedHotkey(state[field.property], field.fallback)); });
                releaseShortcutCapture();
                showFeedback('Settings could not be saved. Please try again.', true);
                updateControls();
            }
        });
    };

    const setShortcutCapture = (input) => {
        const revision = ++shortcutCaptureRevision;
        shortcutCaptureInput = input;
        shortcutCapturePending = true;
        updateControls();
        shortcutCaptureQueue = shortcutCaptureQueue.catch(() => {}).then(async () => {
            try {
                const result = await call('SetHotkeyCapture', !!input);
                if (revision === shortcutCaptureRevision) {
                    shortcutCapturePending = false;
                    await refresh(result);
                }
            } catch (_) {
                if (revision === shortcutCaptureRevision) {
                    if (input) shortcutErrors.set(input.id, 'The shortcut could not be captured. Please try again.');
                    shortcutCaptureInput = null;
                    shortcutCapturePending = false;
                }
                try { await refresh(await call('SetHotkeyCapture', false)); }
                catch (_) { showFeedback('The shortcut could not be restored. Please try again.', true); }
            }
            updateControls();
        });
        return shortcutCaptureQueue;
    };
    const releaseShortcutCapture = () => shortcutCaptureInput || shortcutCapturePending || state.hotkeyCapture ?
        setShortcutCapture(null) : Promise.resolve();
    const captureShortcut = (event, field) => {
        const input = $(field.id);
        if (event.key === 'Tab') return;
        if (event.key === 'Escape') {
            event.preventDefault();
            input.blur();
            return;
        }
        const value = hotkeyFromEvent(event);
        if (!value) return;
        event.preventDefault();
        event.stopPropagation();
        if (event.repeat || shortcutCapturePending || shortcutCaptureInput !== input) return;
        if (shortcutFields.some((entry) => entry !== field && value === normalizedHotkey($(entry.id).value, entry.fallback))) {
            shortcutErrors.set(field.id, 'Choose a different shortcut for each action.');
            updateControls();
            return;
        }
        shortcutErrors.delete(field.id);
        input.value = value;
        saveOptions({ target: input });
    };

    const changeTrackedItem = async (action) => {
        const tracking = currentTracking();
        if (!canChangeTracking(tracking, action)) return;
        const stockAction = stockActions.find((entry) => entry.id === action);
        const target = { itemUid: state.item.uid, charUid: state.characterUid };
        const context = progressContext();
        changingTracking = { ...target, action };
        trackingFailure = null;
        localBusy = true;
        renderTracking();
        updateControls();
        try {
            const result = await call(stockAction.method, target.itemUid);
            if (sameTrackingTarget(target) && context === progressContext()) await refresh(result);
        } catch (_) {
            if (sameTrackingTarget(target) && context === progressContext()) trackingFailure = { ...target, stateSequence: latestStateSequence, message: 'Stock could not be updated. Refresh progress before trying again.' };
        } finally {
            changingTracking = null;
            localBusy = false;
            renderTracking();
            updateControls();
        }
    };

    const toggleAPIKey = async () => {
        if ((!nativeApp() && !preview) || isBusy() || !$('settings').open) return;
        if (keyVisible || keyRevealPending) {
            resetKeyDisplay();
            updateControls();
            return;
        }
        if ($('api-key').value) {
            keyVisible = true;
            updateControls();
            return;
        }
        if (!state.hasAPIKey) return;
        const revision = ++keyRevealRevision;
        keyRevealPending = true;
        updateControls();
        try {
            const key = await call('RevealAPIKey');
            if (revision !== keyRevealRevision || !$('settings').open || $('api-key').value || !state.hasAPIKey || localBusy) return;
            if (typeof key === 'string' && key) {
                $('api-key').value = key;
                keyFromStorage = true;
                keyVisible = true;
            } else {
                showFeedback('The saved API key is unavailable.', true);
            }
        } catch (_) {
            if (revision === keyRevealRevision) showFeedback('The API key could not be shown. Please try again.', true);
        } finally {
            if (revision === keyRevealRevision) keyRevealPending = false;
            updateControls();
        }
    };

    window.addEventListener('DOMContentLoaded', async () => {
        $('item-image').addEventListener('load', () => {
            $('item-image').classList.remove('hidden');
            $('item-image-placeholder').classList.add('hidden');
        });
        $('item-image').addEventListener('error', () => {
            $('item-image').classList.add('hidden');
            $('item-image-placeholder').classList.remove('hidden');
        });
        $('btn-min').addEventListener('click', () => window.runtime && window.runtime.WindowMinimise());
        $('btn-close').addEventListener('click', async () => {
            try { await call('Quit'); }
            catch (_) { showFeedback('The app could not be closed.', true); }
        });
        $('btn-item-page').addEventListener('click', () => action('OpenItemPage'));
        $('btn-copy-item').addEventListener('click', () => copyName(safeText(state.item && state.item.name) || safeText(state.item && state.item.shortName)));
        $('search-form').addEventListener('submit', (event) => {
            event.preventDefault();
            const name = $('search-name').value.trim();
            if (name && state.hasAPIKey) action('SearchItem', [name]);
        });
        $('search-name').addEventListener('input', updateControls);
        $('api-key').addEventListener('input', () => {
            ++keyRevealRevision;
            keyRevealPending = false;
            keyFromStorage = false;
            updateControls();
        });
        $('btn-toggle-key').addEventListener('click', toggleAPIKey);
        $('settings').addEventListener('toggle', () => {
            if (!$('settings').open) {
                releaseShortcutCapture();
                resetKeyDisplay(keyFromStorage);
                updateControls();
            }
        });
        $('btn-scan').addEventListener('click', () => action('ScanNow'));
        $('btn-save-key').addEventListener('click', async () => {
            const key = $('api-key').value.trim();
            if (!key || keyFromStorage || isBusy()) return;
            resetKeyDisplay();
            if (await action('SaveAPIKey', [key], 'API key saved.')) resetKeyDisplay(true);
            updateControls();
        });
        $('btn-clear-key').addEventListener('click', async () => {
            if (isBusy()) return;
            resetKeyDisplay(true);
            await action('ClearAPIKey', [], 'API key cleared.');
            updateControls();
        });
        ['scan-automatic', 'game-mode', 'tracked-character'].forEach((id) => $(id).addEventListener('change', saveOptions));
        shortcutFields.forEach((field) => {
            const input = $(field.id);
            input.addEventListener('focus', () => {
                shortcutErrors.delete(field.id);
                setShortcutCapture(input);
            });
            input.addEventListener('blur', () => { if (shortcutCaptureInput === input) releaseShortcutCapture(); });
            input.addEventListener('keydown', (event) => captureShortcut(event, field));
            $('btn-disable-' + field.id).addEventListener('click', () => {
                shortcutErrors.delete(field.id);
                input.value = 'Disabled';
                saveOptions({ target: input });
            });
        });
        window.addEventListener('blur', releaseShortcutCapture);
        window.addEventListener('focus', () => {
            const field = shortcutFields.find((entry) => $(entry.id) === document.activeElement);
            if (field && $('settings').open && !$(field.id).disabled) setShortcutCapture($(field.id));
        });
        window.addEventListener('pagehide', () => { if (shortcutCaptureInput || state.hotkeyCapture) call('SetHotkeyCapture', false).catch(() => {}); });
        $('btn-refresh-local').addEventListener('click', () => action('RefreshLocalProgress'));
        $('tracker-inventory').addEventListener('toggle', () => {
            if ($('tracker-inventory').open) synchronizeInventory();
        });
        $('btn-refresh-inventory').addEventListener('click', refreshInventoryFromAPI);
        $('btn-import-inventory').addEventListener('click', () => transferInventory(true));
        $('btn-export-inventory').addEventListener('click', () => transferInventory(false));
        $('inventory-search').addEventListener('input', renderInventory);
        $('preview-label').classList.toggle('hidden', !preview);
        try {
            if (!preview && window.runtime && typeof window.runtime.EventsOn === 'function') window.runtime.EventsOn('item-state', render);
            if (nativeApp() || preview) await refresh(await call('GetState'));
            else {
                render({ ...state, status: 'Open this window from the app to use it.' });
            }
        } catch (_) {
            render(state);
            showFeedback('Item Scanner could not start. Restart the app.', true);
        }
    });
})();
