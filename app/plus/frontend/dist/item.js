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
    const shortcutErrors = new Map();
    let shortcutCaptureInput = null;
    let shortcutCapturePending = false;
    let shortcutCaptureRevision = 0;
    let shortcutCaptureQueue = Promise.resolve();
    const shortcutFields = [
        { id: 'add-hotkey', property: 'addHotkey', fallback: 'F1', ready: 'addHotkeyReady', error: 'addHotkeyError' },
        { id: 'remove-hotkey', property: 'removeHotkey', fallback: 'F2', ready: 'removeHotkeyReady', error: 'removeHotkeyError' },
    ];
    let state = {
        enabled: true, automatic: true, language: 'en', mode: 'PVP', hasAPIKey: false,
        addHotkey: 'F1', addHotkeyReady: false, addHotkeyError: '', hotkeyCapture: false,
        removeHotkey: 'F2', removeHotkeyReady: false, removeHotkeyError: '',
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
    const canChangeTracking = (tracking, action) => {
        const current = currentTracking();
        if (state.hotkeyCapture || shortcutCapturePending || !state.local || state.local.loading || safeText(state.local.error)) return false;
        return (!!nativeApp() || preview) && !isBusy() && !safeText(state.error) && state.hasAPIKey && !!state.characterUid &&
            !!safeText(state.item && state.item.uid) &&
            !!safeText(state.item && state.item.bsgId) &&
            !!tracking && sameTrackingTarget(tracking) && tracking.itemId === state.item.bsgId && tracking.tracked === true &&
            tracking.firInventorySeparate !== true &&
            (action === 'remove' ? Number.isSafeInteger(tracking.availableQuantity) && tracking.availableQuantity > 0 && tracking.removeAllowed === true :
                Number.isSafeInteger(tracking.requiredQuantity) && tracking.requiredQuantity > 0 && tracking.addAllowed === true) &&
            !tracking.loading && !tracking.adding && !safeText(tracking.error) && !sameTrackingTarget(trackingFailure) &&
            pendingOptions.size === 0 && !(current && Array.isArray(current.variants) && current.variants.length > 1);
    };
    const canAddTracking = (tracking) => canChangeTracking(tracking, 'add');
    const canRemoveTracking = (tracking) => canChangeTracking(tracking, 'remove');
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
        button.title = kind === 'quest' ? 'Copy quest name' : 'Copy hideout name';
        button.setAttribute('aria-label', 'Copy ' + name);
        button.innerHTML = '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M5 5h8v8H5ZM10 5V2H2v8h3" /></svg>';
        button.addEventListener('click', () => copyName(name));
        return button;
    };
    const trackingCards = new Map();
    const trackingDisplayBaselines = new WeakMap();
    const nextItemCategories = new Set(['Regular', 'Found in Raid', 'Shared', 'Shared (FiR)']);
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
    const currentNextItem = (tracking, multipleVariants) => {
        const key = nextItemContextKey();
        const current = currentTracking();
        const pending = state.pendingAdds && state.pendingAdds.running === true && Number.isSafeInteger(state.pendingAdds.count) && state.pendingAdds.count > 0;
        const transient = !!tracking && (tracking.loading || tracking.adding || !!state.local && state.local.loading ||
            isBusy() || pending || sameTrackingTarget(changingTracking));
        const invalid = !key || !tracking || !sameTrackingTarget(tracking) ||
            safeText(tracking.itemId) && tracking.itemId !== state.item.bsgId ||
            tracking.firInventorySeparate === true || multipleVariants || current && Array.isArray(current.variants) && current.variants.length > 1;
        const error = safeText(state.error) || safeText(state.local && state.local.error) || safeText(tracking && tracking.error) || sameTrackingTarget(trackingFailure);
        if (invalid || error) {
            nextItemCache = null;
            return null;
        }
        if (transient && nextItemCache) return nextItemCache;
        const forecast = tracking.nextItem && typeof tracking.nextItem === 'object' && !Array.isArray(tracking.nextItem) ? tracking.nextItem : null;
        if (forecast && ['estimated', 'uncertain', 'unavailable'].includes(forecast.status)) {
            const category = forecast.status === 'estimated' && nextItemCategories.has(forecast.category) ? forecast.category : '—';
            const targets = Array.isArray(forecast.targets) ? forecast.targets.filter((target) =>
                target && typeof target === 'object' && !Array.isArray(target) &&
                (target.kind === 'quest' || target.kind === 'module') && Number.isSafeInteger(target.requiredQuantity) && target.requiredQuantity >= 0 &&
                (target.allocationUnknown === true || Number.isSafeInteger(target.availableQuantity) && target.availableQuantity >= 0)).map((target) => ({
                    kind: target.kind, name: safeText(target.goalName).trim() || (target.kind === 'quest' ? 'Unnamed quest' : 'Unnamed hideout upgrade'),
                    availableQuantity: target.availableQuantity, requiredQuantity: target.requiredQuantity, allocationUnknown: target.allocationUnknown === true,
                    completed: target.completed === true,
                })).sort((a, b) => a.kind === b.kind ? 0 : a.kind === 'quest' ? -1 : 1) : [];
            if (targets.length) {
                const noRawGap = Number.isSafeInteger(tracking.availableQuantity) && Number.isSafeInteger(tracking.requiredQuantity) &&
                    tracking.availableQuantity >= 0 && tracking.requiredQuantity >= 0 && tracking.availableQuantity >= tracking.requiredQuantity;
                const fullyCovered = tracking.addAllowed === false && noRawGap && targets.every((target) =>
                    !target.allocationUnknown && target.availableQuantity >= target.requiredQuantity);
                const completedState = !transient && tracking.addAllowed === false && tracking.requiredQuantity === 0 && targets.every((target) => target.completed);
                const displayCategory = completedState || fullyCovered && !transient ? '' : category;
                nextItemCache = {category: displayCategory, targets, completedState};
                return nextItemCache;
            }
        }
        if (transient) return nextItemCache;
        nextItemCache = null;
        return null;
    };
    const renderNextItem = (panel, forecast) => {
        const nextItem = panel.querySelector('[data-tracked="next-item"]');
        const category = panel.querySelector('[data-tracked="next-item-category"]');
        const label = forecast && forecast.completedState ? 'Completed Item' : 'Next Item';
        panel.querySelector('[data-tracked="next-item-label"]').textContent = label;
        panel.querySelector('.tracked-stock-list').classList.toggle('hidden', !!(forecast && forecast.completedState));
        panel.querySelector('[data-tracked="add"]').classList.toggle('hidden', !!(forecast && forecast.completedState));
        panel.querySelector('[data-tracked="remove"]').classList.toggle('hidden', !!(forecast && forecast.completedState));
        nextItem.classList.toggle('completed', !!(forecast && forecast.completedState));
        const baseline = trackingDisplayBaselines.get(panel);
        if (baseline) {
            panel.querySelector('[data-tracked="ratio"]').textContent = forecast && forecast.completedState ? '0/0' : baseline.stockRatio;
            panel.querySelector('[data-tracked="requirements"]').classList.toggle('hidden', !!(forecast && forecast.completedState) || !baseline.hasRequirements);
        }
        category.textContent = forecast ? forecast.category : '';
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
        nextItem.classList.toggle('hidden', !forecast);
        if (forecast) nextItem.setAttribute('aria-label', label + (forecast.category ? ': ' + forecast.category : ''));
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
        const status = failure ? '' : changing ? changingTracking && changingTracking.action === 'remove' ? 'Removing…' : 'Updating stock…' : loading ?
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
                    total = { fir, availableQuantity: 0, requiredQuantity: 0, sources: [] };
                    totals.push(total);
                }
                const available = total.availableQuantity + group.availableQuantity;
                const required = total.requiredQuantity + group.requiredQuantity;
                total.availableQuantity = Number.isSafeInteger(available) ? available : NaN;
                total.requiredQuantity = Number.isSafeInteger(required) ? required : NaN;
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
        const separateInventory = tracked && tracking.firInventorySeparate === true;
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
        // Shared rows report confirmed API progress, which can lag pending stock.
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
            quantity.textContent = quantityText(group.availableQuantity) + '/' + quantityText(group.requiredQuantity);
            row.setAttribute('aria-label', label.textContent + ' ' + quantity.textContent);
            row.append(label, quantity);
            return row;
        }));
        element('requirements').classList.toggle('hidden', !visibleRequirements.length && !anyGroups.length);
        const add = element('add');
        add.disabled = !canAddTracking(tracking);
        add.title = multipleVariants ? 'Tarkov Market API cannot update same-ID variants separately.' :
            'Add 1 item to ' + (separateInventory ? '' : 'shared ') + 'Tracked Items stock for the selected character';
        const remove = element('remove');
        remove.disabled = !canRemoveTracking(tracking);
        remove.title = multipleVariants ? 'Tarkov Market API cannot update same-ID variants separately.' :
            'Remove 1 item from shared Tracked Items stock for the selected character';
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
                panel.querySelector('[data-tracked="add"]').addEventListener('click', () => changeTrackedItem('add'));
                panel.querySelector('[data-tracked="remove"]').addEventListener('click', () => changeTrackedItem('remove'));
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
            panel.querySelector('[data-tracked="add"]').disabled = !canAddTracking(entries[index]);
            panel.querySelector('[data-tracked="remove"]').disabled = !canRemoveTracking(entries[index]);
            const badge = panel.querySelector('[data-tracked="add-hotkey"]');
            badge.textContent = hotkeys[0].value;
            badge.classList.toggle('hidden', !hotkeys[0].ready);
            const removeBadge = panel.querySelector('[data-tracked="remove-hotkey"]');
            removeBadge.textContent = hotkeys[1].value;
            removeBadge.classList.toggle('hidden', !hotkeys[1].ready);
            updateNextItem(panel, entries[index], entries.length > 1);
        });
        $('btn-min').disabled = !window.runtime || !window.runtime.WindowMinimise;
        $('btn-close').disabled = !nativeApp();
        $('btn-scan').querySelector('span').textContent = busy ? 'Processing…' : 'Scan now';
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
        ...state, hasAPIKey: true, addHotkey: 'F1', addHotkeyReady: true, removeHotkey: 'F2', removeHotkeyReady: true, status: 'Item recognized.', ocrLanguages: ['en'], cachedAt: '2026-10-10T22:30:23Z', item: sampleItem,
        characterUid: 'preview-character', characters: [{ charUid: 'preview-character', name: 'Sample character', gameMode: 'PVP' }],
        tracking: { itemUid: sampleItem.uid, itemId: 'preview-tracked-item', charUid: 'preview-character',
            tracked: true, loading: false, adding: false, availableQuantity: 0, requiredQuantity: 7,
            remainingQuantity: 7, requirement: 'fir', requirements: [{ kind: 'fir', requiredQuantity: 7 }], error: '', status: '' },
    };
    if (previewVariants) {
        const variantError = 'Separate stock updates are unavailable for variants sharing an item ID in the current Tarkov Market API.';
        const regular = { ...previewState.tracking, variantName: 'MF-UNTAR body armor (0/50)', availableQuantity: 0,
            requiredQuantity: 1, remainingQuantity: 1, requirement: 'regular',
            requirements: [{ kind: 'regular', requiredQuantity: 1, availableQuantity: 0 }],
            addAllowed: false, removeAllowed: false, error: variantError };
        const fir = { ...previewState.tracking, variantName: 'MF-UNTAR body armor', availableQuantity: 2,
            requiredQuantity: 0, remainingQuantity: 0, requirement: 'unknown', requirements: [],
            anyGroups: [{ groupId: 'quest:scavenger:body-armor', fir: true, requiredQuantity: 15, availableQuantity: 6,
                sources: [{ questName: 'Scavenger', category: 'Body Armor', requiredQuantity: 15, availableQuantity: 6,
                    items: [{ itemId: 'preview-mf-untar', name: 'MF-UNTAR body armor', availableQuantity: 2 },
                        { itemId: 'preview-6b13', name: '6B13 assault armor (EMR)', availableQuantity: 3 },
                        { itemId: 'preview-interceptor', name: 'Interceptor OTV body armor (UCP)', availableQuantity: 1 }] }] }],
            addAllowed: false, removeAllowed: false, error: variantError };
        previewState.tracking = { ...fir, variants: [regular, fir] };
    }
    if (preview && !previewVariants) {
        previewState.item = { ...sampleItem, name: 'Salewa first aid kit', shortName: 'Salewa', slots: 2,
            price: 0, avg24hPrice: 0, traderPrice: 0, traderName: '', haveMarketData: false };
        previewState.tracking = { ...previewState.tracking, requirement: 'mixed',
            availableQuantity: previewCompletion ? 0 : 2, requiredQuantity: previewCompletion ? 7 : 8, remainingQuantity: previewCompletion ? 7 : 6,
            addAllowed: true, removeAllowed: !previewCompletion, marketAvailableQuantity: previewCompletion ? 0 : 2,
            requirements: [{ kind: 'fir', requiredQuantity: previewCompletion ? 2 : 5, availableQuantity: previewCompletion ? 0 : 2 },
                { kind: 'regular', requiredQuantity: previewCompletion ? 5 : 3, availableQuantity: previewCompletion ? 0 : 2 }] };
    }
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
        if (method === 'SetOptions') {
            const addHotkey = normalizedHotkey(args[0].addHotkey, 'F1');
            const removeHotkey = normalizedHotkey(args[0].removeHotkey, 'F2');
            if (addHotkey !== 'off' && addHotkey === removeHotkey) throw new Error('Duplicate shortcut.');
            previewState = { ...previewState, ...args[0], addHotkey, removeHotkey,
                addHotkeyReady: addHotkey !== 'off' && !previewState.hotkeyCapture, addHotkeyError: '',
                removeHotkeyReady: removeHotkey !== 'off' && !previewState.hotkeyCapture, removeHotkeyError: '' };
        }
        if (method === 'SetHotkeyCapture') previewState = { ...previewState, hotkeyCapture: !!args[0],
            addHotkeyReady: !args[0] && previewState.addHotkey !== 'off', removeHotkeyReady: !args[0] && previewState.removeHotkey !== 'off' };
        if (method === 'RefreshLocalProgress' && previewState.local) {
            if (!previewVariants) {
                const requiredQuantity = previewCompletion ? 4 : 5;
                const firRequired = previewCompletion ? 1 : 2;
                const stock = previewState.tracking.marketAvailableQuantity;
                const requirements = [{ kind: 'fir', requiredQuantity: firRequired, availableQuantity: Math.min(stock, firRequired) },
                    { kind: 'regular', requiredQuantity: 3, availableQuantity: Math.min(3, stock) }];
                const addAllowed = previewState.tracking.availableQuantity < requiredQuantity;
                previewState.tracking = { ...previewState.tracking, requiredQuantity,
                    remainingQuantity: Math.max(0, requiredQuantity - previewState.tracking.availableQuantity),
                    requirements, addAllowed,
                    completedSources: [{ type: 'quest', quantity: 3, optional: false }] };
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
        if ((method === 'AddTrackedItem' || method === 'RemoveTrackedItem') && previewState.tracking &&
            (method === 'RemoveTrackedItem' ? previewState.tracking.removeAllowed : previewState.tracking.addAllowed) && !previewState.tracking.error &&
            !previewState.tracking.variants && args[0] === previewState.tracking.itemUid) {
            const tracking = previewState.tracking;
            const removing = method === 'RemoveTrackedItem';
            const availableQuantity = Math.max(0, tracking.availableQuantity + (removing ? -1 : 1));
            const addAllowed = availableQuantity < tracking.requiredQuantity;
            previewState = { ...previewState, tracking: { ...tracking, availableQuantity,
                addAllowed, removeAllowed: availableQuantity > 0,
                remainingQuantity: Math.max(0, tracking.requiredQuantity - availableQuantity),
                status: (removing ? 'Removed' : 'Added') + ' 1 item. Saving to Tarkov Market…' } };
            const count = (previewState.pendingAdds && previewState.pendingAdds.count || 0) + 1;
            previewState.pendingAdds = { count, running: true, status: 'Saving to Tarkov Market…', error: '' };
            setTimeout(() => {
                const count = Math.max(0, previewState.pendingAdds.count - 1);
                previewState.pendingAdds = { count, running: count > 0, status: count > 0 ? 'Saving to Tarkov Market…' : '', error: '' };
                if (!count && previewState.tracking) {
                    const stock = previewState.tracking.availableQuantity;
                    previewState.tracking = { ...previewState.tracking, marketAvailableQuantity: stock,
                        requirements: previewState.tracking.requirements.map((row) => ({ ...row, availableQuantity: Math.min(stock, row.requiredQuantity) })),
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
        }
    };

    const readOptions = () => ({
        enabled: true, automatic: $('scan-automatic').checked,
        addHotkey: normalizedHotkey($('add-hotkey').value, 'F1'),
        removeHotkey: normalizedHotkey($('remove-hotkey').value, 'F2'),
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
        const other = shortcutFields.find((entry) => entry !== field);
        if (value === normalizedHotkey($(other.id).value, other.fallback)) {
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
        if (!(action === 'remove' ? canRemoveTracking(tracking) : canAddTracking(tracking))) return;
        const target = { itemUid: state.item.uid, charUid: state.characterUid };
        const context = progressContext();
        changingTracking = { ...target, action };
        trackingFailure = null;
        localBusy = true;
        renderTracking();
        updateControls();
        try {
            const result = await call(action === 'remove' ? 'RemoveTrackedItem' : 'AddTrackedItem', target.itemUid);
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
