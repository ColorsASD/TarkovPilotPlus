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
    let addingTracking = null;
    let trackingFailure = null;
    let keyVisible = false;
    let keyFromStorage = false;
    let keyRevealRevision = 0;
    let keyRevealPending = false;
    let initialRender = true;
    let latestStateSequence = null;
    let handoverRequestPending = false;
    let handoverUIError = '';
    let handoverReturnFocus = null;
    let handoverFieldID = 0;
    const handoverRows = new Map();
    const handoverEdits = new Map();
    const invalidHandoverQuantities = new Set();
    const stockEditorCards = new Map();
    const stockEditorDrafts = new Map();
    let stockEditorDetailID = 0;
    const stockEditorSources = [
        { source: 'market', label: 'Tarkov-Market Progress', stock: 'marketStock', used: 'marketHandedIn', editable: 'marketEditable' },
        { source: 'local', label: 'Local Progress', stock: 'localStock', used: 'localUsed', editable: 'localEditable' },
        { source: 'market-fallback', label: 'Tarkov-Market Progress — pending upload', stock: 'marketFallbackStock', used: 'marketFallbackUsed', editable: 'marketFallbackEditable' },
    ];
    let stockEditorIdentity = '';
    let stockEditorStale = false;
    let stockEditorUIError = '';
    let stockEditorRequest = '';
    let stockEditorRequestSource = '';
    let stockEditorRequestRevision = 0;
    let stockEditorAcceptGeneration = false;
    let stockEditorCloseRequested = false;
    let stockEditorCloseScheduled = false;
    let localBackupRequest = '';
    let localBackupRequestRevision = 0;
    let localBackupUIError = '';
    let marketUploadRetryPending = false;
    let marketUploadRetryRevision = 0;
    const shortcutErrors = new Map();
    let shortcutCaptureInput = null;
    let shortcutCapturePending = false;
    let shortcutCaptureRevision = 0;
    let shortcutCaptureQueue = Promise.resolve();
    const shortcutFields = [
        { id: 'add-hotkey', property: 'addHotkey', fallback: 'F2', ready: 'addHotkeyReady', error: 'addHotkeyError' },
        { id: 'fir-hotkey', property: 'firHotkey', fallback: 'F1', ready: 'firHotkeyReady', error: 'firHotkeyError' },
    ];
    let state = {
        enabled: true, automatic: true, language: 'en', mode: 'PVP', hasAPIKey: false,
        addHotkey: 'F2', addHotkeyReady: false, addHotkeyError: '',
        firHotkey: 'F1', firHotkeyReady: false, firHotkeyError: '', hotkeyCapture: false,
        busy: false, status: 'no_key', error: '', ocrLanguages: [], cachedAt: '', stale: false, item: null, scanDetails: null,
        characterUid: '', characters: [], charactersLoading: false, charactersError: '', tracking: null,
        localProgress: true, local: null, handover: null, stockEditor: null, localBackup: null, marketFallbackPending: false,
        marketFallbackItemId: '', marketFallbackItemIds: undefined, marketFallbackInvalid: false, marketFallbackAppendAllowed: false,
        marketFallbackAppendAllowedItemIds: undefined, progressSync: null,
    };

    const statusNames = {
        idle: 'Waiting for an item Inspect window.', ready: 'Waiting for an item Inspect window.', disabled: 'Automatic scanning is off.',
        scanning: 'Inspect title scanning…', searching: 'Searching for item…', no_key: 'API key required',
        no_match: 'Item not found', waiting: 'Waiting for the game',
    };

    const isBusy = () => localBusy || !!state.busy || handoverProcessing() || stockEditorProcessing() || settingsOperationBusy();
    const localBackupProcessing = () => !!localBackupRequest || !!(state.localBackup && (state.localBackup.busy || state.localBackup.recoveryRequired === true));
    const settingsOperationBusy = () => localBackupProcessing() || marketUploadRetryPending;
    const safeText = (value) => typeof value === 'string' ? value.slice(0, 500) : '';
    const handoverProcessing = () => handoverRequestPending || !!(state.handover && state.handover.busy);
    const handoverProgressLoading = () => !!state.charactersLoading || !!(state.local && state.local.loading) || !!(state.tracking && state.tracking.loading);
    const handoverItems = () => state.handover && Array.isArray(state.handover.items) ? state.handover.items.filter((item) =>
        item && typeof item === 'object' && safeText(item.id)) : [];
    const scopedHandoverItems = () => {
        const handover = state.handover;
        if (!handover || handover.scoped !== true || !handover.pending || !Array.isArray(handover.items) || !handover.items.length) return null;
        const items = handoverItems();
        return items.length === handover.items.length && items.every((item) => safeText(item.itemId).trim()) ?
            new Set(items.map((item) => safeText(item.itemId).trim())) : null;
    };
    const handoverBlocksAll = () => !!(state.handover && (state.handover.open || state.handover.pending) && !scopedHandoverItems());
    const handoverBlocksItem = (itemId) => {
        if (!state.handover || !state.handover.open && !state.handover.pending) return false;
        const items = scopedHandoverItems();
        return !items || items.has(safeText(itemId).trim());
    };
    const validHandoverQuantity = (value) => Number.isSafeInteger(value) && value > 0 && value <= 1000000000;
    const stockEditorState = () => state.stockEditor && typeof state.stockEditor === 'object' ? state.stockEditor : null;
    const stockEditorPendingBlocksAll = () => !!(stockEditorState() && stockEditorState().pending && !safeText(stockEditorState().pendingItemId).trim());
    const stockEditorPendingBlocksItem = (itemId) => stockEditorPendingBlocksAll() || !!(stockEditorState() && stockEditorState().pending &&
        safeText(stockEditorState().pendingItemId).trim() === safeText(itemId).trim());
    const marketUploadRetryBlocked = () => !!(stockEditorState() && stockEditorState().pending) || !!(state.handover && (state.handover.open || state.handover.pending)) ||
        !!(progressSyncState() && progressSyncState().running);
    const stockEditorProcessing = () => !!stockEditorRequest || !!(stockEditorState() && (stockEditorState().loading || stockEditorState().saving));
    const pendingItemIDs = (value) => Array.isArray(value) && value.every((id) => typeof id === 'string' && id.length <= 500 && id.trim()) ?
        new Set(value.map((id) => id.trim())) : null;
    const marketFallbackItems = () => {
        if (state.marketFallbackInvalid === true) return null;
        if (state.marketFallbackItemIds !== undefined) {
            const items = pendingItemIDs(state.marketFallbackItemIds);
            return items && items.size ? items : null;
        }
        const id = safeText(state.marketFallbackItemId).trim();
        return id ? new Set([id]) : null;
    };
    const marketFallbackBlocksAll = () => state.marketFallbackPending === true && !marketFallbackItems();
    const marketFallbackBlocksItem = (itemId) => state.marketFallbackPending === true &&
        (!marketFallbackItems() || marketFallbackItems().has(safeText(itemId).trim()));
    const marketFallbackAllowsAppend = (itemId) => {
        const items = marketFallbackItems();
        const id = safeText(itemId).trim();
        if (!items || !items.has(id)) return false;
        if (state.marketFallbackAppendAllowedItemIds !== undefined) {
            const allowed = pendingItemIDs(state.marketFallbackAppendAllowedItemIds);
            return !!allowed && allowed.has(id);
        }
        return state.marketFallbackItemIds === undefined && state.marketFallbackAppendAllowed === true;
    };
    const stockEditorBlocksStock = () => marketFallbackBlocksAll() || stockEditorPendingBlocksAll() || !!stockEditorRequest || !!(stockEditorState() && stockEditorState().open);
    const progressSyncState = () => state.progressSync && typeof state.progressSync === 'object' ? state.progressSync : null;
    const progressSyncMessage = () => {
        const sync = progressSyncState();
        return sync ? safeText(sync.error) || safeText(sync.status) : '';
    };
    const validStockQuantity = (value) => Number.isSafeInteger(value) && value >= 0 && value <= 1000000000;
    const stockEditorContext = () => JSON.stringify([state.characterUid, String(state.mode || '').toUpperCase(), optionsRevision]);
    const normalizedHotkey = (value, fallback = 'F2') => {
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
        return tracking && typeof tracking === 'object' && state.item &&
            tracking.itemUid === state.item.uid && tracking.charUid === state.characterUid ? tracking : null;
    };
    const sameTrackingTarget = (target) => !!target && !!state.item &&
        target.itemUid === state.item.uid && target.charUid === state.characterUid;
    const trackingEntries = () => {
        const tracking = currentTracking();
        if (!tracking) return [];
        return Array.isArray(tracking.variants) && tracking.variants.length ? tracking.variants.filter((variant) =>
            variant && typeof variant === 'object' && sameTrackingTarget(variant)) : [tracking];
    };
    const canAddTracking = (tracking, kind = 'regular') => {
        const current = currentTracking();
        if (stockEditorBlocksStock()) return false;
        if (tracking && (stockEditorPendingBlocksItem(tracking.itemId) || handoverBlocksItem(tracking.itemId))) return false;
        if (tracking && marketFallbackBlocksItem(tracking.itemId) && tracking.storage !== 'local' && !marketFallbackAllowsAppend(tracking.itemId)) return false;
        if (handoverProcessing()) return false;
        if (state.hotkeyCapture || shortcutCapturePending || !state.local || state.local.loading || state.local.stockError === true || safeText(state.local.error)) return false;
        return (!!nativeApp() || preview) && !isBusy() && !safeText(state.error) && state.hasAPIKey && !!state.characterUid &&
            !!safeText(state.item && state.item.uid) &&
            !!safeText(state.item && state.item.bsgId) &&
            !!tracking && sameTrackingTarget(tracking) && tracking.itemId === state.item.bsgId && tracking.tracked === true &&
            Number.isSafeInteger(tracking.requiredQuantity) && tracking.requiredQuantity > 0 && tracking.firInventorySeparate !== true &&
            (kind === 'fir' ? tracking.addFIRAllowed === true : kind === 'regular' && tracking.addRegularAllowed === true) &&
            !tracking.loading && !tracking.adding && !safeText(tracking.error) && !sameTrackingTarget(trackingFailure) &&
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

    const trackingCards = new Map();
    const renderTrackingCard = (panel, tracking, multipleVariants, variantLabel) => {
        const element = (name) => panel.querySelector('[data-tracked="' + name + '"]');
        const tracked = !!tracking && tracking.tracked === true;
        const adding = sameTrackingTarget(addingTracking) || !!tracking && !!tracking.adding;
        const failure = sameTrackingTarget(trackingFailure) ? trackingFailure.message : '';
        const error = failure || safeText(tracking && tracking.error);
        const status = adding ? 'Adding…' : tracking && tracking.loading ?
            'Loading Tracked Items data…' : safeText(tracking && tracking.status);
        const requirements = tracked && Array.isArray(tracking.requirements) ? tracking.requirements.filter((requirement) =>
            requirement && (requirement.kind === 'fir' || requirement.kind === 'regular') &&
            Number.isInteger(requirement.requiredQuantity) && requirement.requiredQuantity >= 0).sort((a, b) =>
                a.kind === b.kind ? 0 : a.kind === 'fir' ? -1 : 1) : [];
        const noRequiredQuantity = tracked && tracking.requiredQuantity === 0;
        const noRemainingRequirement = noRequiredQuantity || tracked &&
            Number.isSafeInteger(tracking.remainingQuantity) && tracking.remainingQuantity === 0;
        const visibleRequirements = noRequiredQuantity ? [] : requirements;
        const anyGroups = tracked && !noRequiredQuantity && Array.isArray(tracking.anyGroups) ? tracking.anyGroups.filter((group) =>
            group && Number.isSafeInteger(group.requiredQuantity) && group.requiredQuantity > 0 &&
            Number.isSafeInteger(group.availableQuantity) && group.availableQuantity >= 0 && group.availableQuantity < group.requiredQuantity).reduce((totals, group) => {
                const fir = group.fir === true;
                let total = totals.find((entry) => entry.fir === fir);
                if (!total) {
                    total = { fir, availableQuantity: 0, requiredQuantity: 0 };
                    totals.push(total);
                }
                const available = total.availableQuantity + group.availableQuantity;
                const required = total.requiredQuantity + group.requiredQuantity;
                total.availableQuantity = Number.isSafeInteger(available) ? available : NaN;
                total.requiredQuantity = Number.isSafeInteger(required) ? required : NaN;
                return totals;
            }, []).sort((a, b) =>
                a.fir === true ? b.fir === true ? 0 : -1 : b.fir === true ? 1 : 0) : [];
        const separateInventory = tracked && tracking.firInventorySeparate === true;
        const completedSources = tracked && Array.isArray(tracking.completedSources) ? tracking.completedSources.filter((source) =>
            source && typeof source === 'object' && !Array.isArray(source)) : [];
        panel.classList.toggle('hidden', noRemainingRequirement && !error && !(tracking && tracking.loading) && !adding ||
            !tracked && !error && !(tracking && tracking.loading) && !adding);
        element('content').classList.toggle('hidden', !tracked || noRequiredQuantity);
        const variantName = multipleVariants ? variantLabel : '';
        element('variant-name').textContent = variantName;
        element('variant-name').classList.toggle('hidden', !variantName);
        panel.setAttribute('aria-label', variantName ? 'Items Tracker: ' + variantName : 'Items Tracker');
        element('ratio').textContent = tracked ? noRequiredQuantity ? itemsText(tracking.availableQuantity) :
            quantityText(tracking.availableQuantity) + '/' + quantityText(tracking.requiredQuantity) : '—/—';
        element('ratio').setAttribute('aria-label', noRequiredQuantity ? 'Stock quantity' : 'Available / required quantity');
        element('requirements').replaceChildren(...visibleRequirements.map((requirement) => {
            const row = document.createElement('div');
            row.className = 'tracked-requirement-row';
            row.dataset.kind = requirement.kind;
            const label = document.createElement('dt');
            label.textContent = requirement.kind === 'fir' ? 'Found in Raid' : 'Regular';
            const quantity = document.createElement('dd');
            quantity.textContent = quantityText(requirement.availableQuantity) + '/' + quantityText(requirement.requiredQuantity);
            row.append(label, quantity);
            return row;
        }), ...anyGroups.map((group) => {
            const row = document.createElement('div');
            row.className = 'tracked-requirement-row';
            row.dataset.kind = 'any';
            const label = document.createElement('dt');
            label.textContent = group.fir === true ? 'Any (FiR)' : 'Any';
            const quantity = document.createElement('dd');
            quantity.textContent = quantityText(group.availableQuantity) + '/' + quantityText(group.requiredQuantity);
            row.append(label, quantity);
            return row;
        }));
        element('requirements').classList.toggle('hidden', !visibleRequirements.length && !anyGroups.length);
        element('completed-sources').replaceChildren(...completedSources.map((source) => {
            const row = document.createElement('div');
            row.className = 'tracked-completed-row';
            row.dataset.type = source.type === 'quest' || source.type === 'hideout' ? source.type : 'unknown';
            const label = document.createElement('dt');
            label.textContent = ({ quest: 'Quest completed', hideout: 'Hideout upgrade completed' })[row.dataset.type] || 'Requirement completed';
            if (source.optional === true) label.textContent += ' (optional)';
            row.append(label);
            if (Number.isSafeInteger(source.quantity) && source.quantity > 0) {
                const quantity = document.createElement('dd');
                quantity.textContent = itemsText(source.quantity) + ' completed';
                row.append(quantity);
            }
            return row;
        }));
        element('completed-sources').classList.toggle('hidden', !completedSources.length);
        const add = element('add');
        add.disabled = !canAddTracking(tracking);
        element('add-label').textContent = 'Add Regular item';
        element('add-fir').disabled = !canAddTracking(tracking, 'fir');
        add.title = multipleVariants ? 'Tarkov Market API cannot update same-ID variants separately.' :
            'Add 1 item to ' + (separateInventory ? '' : 'shared ') + 'Tracked Items stock for the selected character';
        element('status').textContent = status;
        element('status').classList.toggle('hidden', !status);
        element('error').textContent = error;
        element('error').classList.toggle('hidden', !error);
    };
    const renderTracking = () => {
        const entries = trackingEntries();
        const panels = $('tracked-panels');
        const keys = new Set();
        const names = entries.map((tracking) => safeText(tracking.variantName).trim());
        entries.forEach((tracking, index) => {
            const key = JSON.stringify([tracking.itemUid, tracking.charUid, tracking.variantName || '', index]);
            keys.add(key);
            let panel = trackingCards.get(key);
            if (!panel) {
                panel = $('tracked-panel-template').content.firstElementChild.cloneNode(true);
                panel.querySelector('[data-tracked="add"]').addEventListener('click', () => addTrackedItem());
                panel.querySelector('[data-tracked="add-fir"]').addEventListener('click', () => addTrackedItem('fir'));
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
        const message = local && local.loading ? 'Refreshing Local Progress…' : error ||
            safeText(local && local.warning) || (local && local.updatedAt ? 'Last sync: ' + localDateText(local.updatedAt) :
                'Select a character and refresh progress.');
        $('local-progress-status').textContent = message;
        $('local-progress-status').classList.toggle('error', !!error);
    };

    const renderLocalBackup = () => {
        const backup = state.localBackup;
        const status = localBackupRequest === 'ExportLocalProgress' ? 'Exporting Local Progress…' :
            localBackupRequest === 'ImportLocalProgress' ? 'Importing Local Progress…' : safeText(backup && backup.status);
        const error = localBackupUIError || safeText(backup && backup.error);
        $('local-backup-status').textContent = status;
        $('local-backup-status').classList.toggle('hidden', !status);
        $('local-backup-error').textContent = error;
        $('local-backup-error').classList.toggle('hidden', !error);
    };

    const renderProgressSync = () => {
        const sync = progressSyncState();
        const validCount = sync && Number.isSafeInteger(sync.pendingCount) && sync.pendingCount >= 0;
        const validItems = sync && Number.isSafeInteger(sync.pendingItems) && sync.pendingItems >= 0;
        $('progress-sync-count').textContent = validCount ? quantityText(sync.pendingCount) : '—';
        $('progress-sync-items').textContent = validItems ? quantityText(sync.pendingItems) : '—';
        const status = safeText(sync && sync.status) || (validCount && sync.pendingCount === 0 && !sync.running && !sync.pending ? 'No pending uploads.' : '');
        const error = safeText(sync && sync.error);
        $('progress-sync-status').textContent = status;
        $('progress-sync-status').classList.toggle('hidden', !status);
        $('progress-sync-error').textContent = error;
        $('progress-sync-error').classList.toggle('hidden', !error);
        const operations = sync && Array.isArray(sync.operations) ? sync.operations.filter((operation) => operation && typeof operation === 'object') : [];
        const rows = operations.map((operation) => {
            const row = document.createElement('li');
            const name = document.createElement('p');
            name.className = 'progress-sync-item';
            name.textContent = safeText(operation.itemName) || safeText(operation.itemId) || 'Saved operation';
            if (Number.isSafeInteger(operation.quantity) && operation.quantity >= 0) name.textContent += ' · ' + itemsText(operation.quantity);
            row.append(name);
            const status = safeText(operation.status);
            const error = safeText(operation.error);
            for (const [text, isError] of [[status, false], [error, true]]) {
                if (!text) continue;
                const message = document.createElement('p');
                message.className = 'hint' + (isError ? ' error' : '');
                message.textContent = text;
                row.append(message);
            }
            return row;
        });
        $('progress-sync-operations').replaceChildren(...rows);
        $('progress-sync-operations').classList.toggle('hidden', !rows.length);
        $('progress-sync-panel').setAttribute('aria-busy', String(!!(sync && sync.running)));
    };

    const stockEditorRows = () => {
        const editor = stockEditorState();
        const seen = new Set();
        return editor && Array.isArray(editor.rows) ? editor.rows.filter((row) => {
            const id = safeText(row && row.itemId);
            if (!row || typeof row !== 'object' || !id || seen.has(id) || !validStockQuantity(row.availableQuantity) || row.availableQuantity <= 0) return false;
            seen.add(id);
            return true;
        }) : [];
    };
    const stockEditorSnapshotIdentity = (editor) => editor && Number.isSafeInteger(editor.generation) && editor.generation > 0 ?
        JSON.stringify([editor.generation, safeText(editor.charUid), String(editor.gameMode || '').toUpperCase()]) : '';
    const stockEditorMatchesContext = (editor) => !!editor && editor.charUid === state.characterUid &&
        String(editor.gameMode || '').toUpperCase() === String(state.mode || '').toUpperCase();
    const stockSourceKey = (itemId, source) => JSON.stringify([itemId, source]);
    const stockValues = (value) => ({ fir: value && value.fir, regular: value && value.regular, unknown: value && value.unknown });
    const stockTotal = (value) => {
        const values = Object.values(stockValues(value));
        if (!values.every(validStockQuantity)) return null;
        const total = values.reduce((sum, quantity) => sum + quantity, 0);
        return validStockQuantity(total) ? total : null;
    };
    const stockSourceValues = (source) => {
        const result = {};
        for (const [kind, input] of source.inputs) result[kind] = input.valueAsNumber;
        return result;
    };
    const updateStockEditorControls = () => {
        const editor = stockEditorState();
        const connected = !!nativeApp() || preview;
        const available = connected && state.hasAPIKey && !!state.characterUid;
        const blocked = localBusy || !!state.busy || !!addingTracking || !!(state.tracking && state.tracking.adding) ||
            handoverProcessing() || settingsOperationBusy() || handoverBlocksAll() || pendingOptions.size > 0;
        const processing = stockEditorProcessing();
        const identity = stockEditorSnapshotIdentity(editor);
        const contextReady = !!identity && stockEditorMatchesContext(editor) && identity === stockEditorIdentity;
        $('stock-editor').querySelector('summary').setAttribute('aria-disabled', String(!connected || blocked || processing));
        $('stock-editor-search').disabled = !available || settingsOperationBusy() || !editor || !editor.open;
        $('btn-refresh-stock-editor').disabled = !available || blocked || processing || !editor || !editor.open;
        $('btn-refresh-stock-editor').textContent = editor && editor.loading ? 'Refreshing…' : 'Refresh';
        $('btn-retry-market-upload').classList.toggle('hidden', state.marketFallbackPending !== true);
        $('market-upload-retry-hint').classList.toggle('hidden', state.marketFallbackPending !== true);
        $('btn-retry-market-upload').disabled = !available || isBusy() || marketUploadRetryBlocked() || pendingOptions.size > 0;
        $('btn-retry-market-upload').textContent = marketUploadRetryPending ? 'Retrying…' : 'Retry saved upload';
        $('btn-discard-stock-editor').classList.toggle('hidden', !(editor && editor.pending));
        $('stock-editor-pending-hint').classList.toggle('hidden', !(editor && editor.pending));
        $('btn-discard-stock-editor').disabled = !available || blocked || processing || !(editor && editor.pending) || editor.background === true;
        for (const card of stockEditorCards.values()) {
            card.edit.disabled = settingsOperationBusy();
            for (const source of card.sources.values()) {
                const editable = card.row && card.row[source.definition.editable] === true;
                const fallbackBlocked = marketFallbackBlocksAll() || marketFallbackBlocksItem(card.row.itemId) && source.source !== 'local';
                const frozen = !available || blocked || processing || !editor || !editor.open || stockEditorPendingBlocksItem(card.row.itemId) ||
                    handoverBlocksItem(card.row.itemId) || fallbackBlocked || !contextReady || stockEditorStale || !editable || source.fieldset.classList.contains('hidden');
                const values = stockSourceValues(source);
                const total = stockTotal(values);
                source.inputs.forEach((input) => {
                    input.disabled = frozen;
                    input.setAttribute('aria-invalid', String(!validStockQuantity(input.valueAsNumber)));
                });
                source.total.textContent = total === null ? '—' : quantityText(total);
                source.total.classList.toggle('error', total === null);
                source.save.disabled = frozen || total === null;
                source.clear.disabled = frozen;
                source.save.textContent = stockEditorRequest === 'SaveStockEditorItem' && stockEditorRequestSource === stockSourceKey(card.row.itemId, source.source) ? 'Saving…' : 'Save';
            }
        }
        $('stock-editor-rows').setAttribute('aria-busy', String(processing));
    };
    const createStockEditorCard = (row) => {
        const element = document.createElement('tr');
        element.className = 'stock-editor-item';
        element.dataset.itemId = row.itemId;
        const name = document.createElement('th');
        name.scope = 'row';
        const stock = document.createElement('td');
        const used = document.createElement('td');
        const editCell = document.createElement('td');
        const edit = document.createElement('button');
        edit.type = 'button';
        edit.className = 'stock-editor-edit';
        edit.textContent = 'Edit';
        edit.setAttribute('aria-expanded', 'false');
        editCell.append(edit);
        element.append(name, stock, used, editCell);
        const detail = document.createElement('tr');
        detail.className = 'stock-editor-detail hidden';
        detail.dataset.itemId = row.itemId;
        detail.id = 'stock-editor-detail-' + (++stockEditorDetailID);
        edit.setAttribute('aria-controls', detail.id);
        const detailCell = document.createElement('td');
        detailCell.colSpan = 4;
        const content = document.createElement('div');
        content.className = 'stock-editor-detail-content';
        detailCell.append(content);
        detail.append(detailCell);
        const summary = document.createElement('dl');
        summary.className = 'stock-editor-summary';
        const counters = new Map();
        for (const label of ['Stock', 'Required', 'Used', 'Market accounted']) {
            const line = document.createElement('div');
            const title = document.createElement('dt');
            const quantity = document.createElement('dd');
            title.textContent = label;
            line.append(title, quantity);
            summary.append(line);
            counters.set(label, quantity);
        }
        content.append(summary);
        const sources = new Map();
        for (const definition of stockEditorSources) {
            const sourceName = definition.source;
            const fieldset = document.createElement('fieldset');
            fieldset.className = 'stock-editor-source';
            fieldset.dataset.source = sourceName;
            const legend = document.createElement('legend');
            legend.textContent = definition.label;
            fieldset.append(legend);
            let note = null;
            if (sourceName === 'market-fallback') {
                note = document.createElement('p');
                note.className = 'hint stock-editor-source-note';
                note.textContent = 'Saved locally until Tarkov Market confirms the upload.';
                fieldset.append(note);
            }
            const usage = document.createElement('details');
            usage.className = 'stock-editor-usage';
            const usageSummary = document.createElement('summary');
            usageSummary.append(document.createTextNode('Used'));
            const usageTotal = document.createElement('span');
            usageSummary.append(usageTotal);
            const usageBreakdown = document.createElement('dl');
            usageBreakdown.className = 'stock-editor-summary';
            const usageCounters = new Map();
            for (const [kind, labelText] of [['fir', 'Found in Raid'], ['regular', 'Regular'], ['unknown', 'Unclassified']]) {
                const line = document.createElement('div');
                const label = document.createElement('dt');
                label.textContent = labelText;
                const quantity = document.createElement('dd');
                line.append(label, quantity);
                usageBreakdown.append(line);
                usageCounters.set(kind, quantity);
            }
            usage.append(usageSummary, usageBreakdown);
            fieldset.append(usage);
            const inputs = new Map();
            for (const [kind, labelText] of [['fir', 'Found in Raid'], ['regular', 'Regular'], ['unknown', 'Unclassified']]) {
                const label = document.createElement('label');
                label.className = 'stock-editor-field';
                const caption = document.createElement('span');
                caption.textContent = labelText;
                const input = document.createElement('input');
                input.type = 'number';
                input.min = '0';
                input.max = '1000000000';
                input.step = '1';
                input.inputMode = 'numeric';
                input.dataset.kind = kind;
                input.addEventListener('input', () => {
                    const key = stockSourceKey(row.itemId, sourceName);
                    stockEditorDrafts.set(key, { values: Object.fromEntries([...inputs].map(([bucket, field]) => [bucket, field.value])), dirty: true });
                    updateStockEditorControls();
                });
                label.append(caption, input);
                fieldset.append(label);
                inputs.set(kind, input);
            }
            const totalRow = document.createElement('dl');
            totalRow.className = 'stock-editor-total';
            const totalLabel = document.createElement('dt');
            totalLabel.textContent = 'Total';
            const total = document.createElement('dd');
            totalRow.append(totalLabel, total);
            const actions = document.createElement('div');
            actions.className = 'stock-editor-actions';
            const save = document.createElement('button');
            save.type = 'button';
            save.textContent = 'Save';
            save.addEventListener('click', () => saveStockEditorSource(row.itemId, sourceName));
            const clear = document.createElement('button');
            clear.type = 'button';
            clear.textContent = 'Clear stock';
            clear.className = 'subtle-button';
            clear.addEventListener('click', () => saveStockEditorSource(row.itemId, sourceName, true));
            actions.append(save, clear);
            fieldset.append(totalRow, actions);
            content.append(fieldset);
            sources.set(sourceName, { source: sourceName, definition, fieldset, inputs, total, save, clear, usageTotal, usageCounters, note });
        }
        const error = document.createElement('p');
        error.className = 'hint error hidden';
        error.setAttribute('role', 'alert');
        content.append(error);
        const card = { element, name, stock, used, edit, detail, counters, sources, error, row, detailsOpen: false };
        edit.addEventListener('click', () => {
            card.detailsOpen = !card.detailsOpen;
            edit.setAttribute('aria-expanded', String(card.detailsOpen));
            filterStockEditor();
        });
        return card;
    };
    const filterStockEditor = () => {
        const search = $('stock-editor-search').value.trim().toLowerCase();
        let visible = 0;
        for (const card of stockEditorCards.values()) {
            const matches = !search || (safeText(card.row.name) + ' ' + safeText(card.row.itemId)).toLowerCase().includes(search);
            card.element.classList.toggle('hidden', !matches);
            card.detail.classList.toggle('hidden', !matches || !card.detailsOpen);
            if (matches) ++visible;
        }
        const editor = stockEditorState();
        $('stock-editor-empty').classList.toggle('hidden', !editor || !editor.open || editor.loading || visible > 0);
        $('stock-editor-table').classList.toggle('hidden', visible === 0);
    };
    const renderStockEditor = () => {
        const focused = document.activeElement;
        const editor = stockEditorState();
        if (!stockEditorRequest) $('stock-editor').open = !!(editor && editor.open);
        const identity = stockEditorSnapshotIdentity(editor);
        if (!editor || !editor.open) {
            if (!stockEditorRequest) {
                stockEditorCards.clear();
                stockEditorDrafts.clear();
                stockEditorIdentity = '';
                stockEditorStale = false;
                $('stock-editor-rows').replaceChildren();
            }
        } else if (identity && identity !== stockEditorIdentity) {
            const changedContext = stockEditorIdentity && JSON.parse(stockEditorIdentity).slice(1).join('|') !== JSON.parse(identity).slice(1).join('|');
            const dirty = [...stockEditorDrafts.values()].some((draft) => draft.dirty);
            if (!stockEditorIdentity || changedContext || !dirty || stockEditorAcceptGeneration || localBackupRequest === 'ImportLocalProgress' || marketUploadRetryPending) {
                stockEditorIdentity = identity;
                stockEditorStale = false;
                stockEditorDrafts.clear();
            } else stockEditorStale = true;
        }
        const rows = editor && editor.open ? stockEditorRows() : [];
        const keys = new Set(rows.map((row) => row.itemId));
        for (const [key, card] of stockEditorCards) {
            if (!keys.has(key)) {
                card.element.remove();
                card.detail.remove();
                stockEditorCards.delete(key);
                for (const source of card.sources.keys()) stockEditorDrafts.delete(stockSourceKey(key, source));
            }
        }
        rows.forEach((row, index) => {
            let card = stockEditorCards.get(row.itemId);
            if (!card) {
                card = createStockEditorCard(row);
                stockEditorCards.set(row.itemId, card);
            }
            card.row = row;
            card.name.textContent = safeText(row.name) || safeText(row.itemId);
            card.stock.textContent = quantityText(row.availableQuantity);
            card.used.textContent = quantityText(row.usedQuantity);
            card.edit.setAttribute('aria-label', 'Edit stock for ' + card.name.textContent);
            card.counters.get('Stock').textContent = quantityText(row.availableQuantity);
            card.counters.get('Required').textContent = quantityText(row.requiredQuantity);
            card.counters.get('Used').textContent = quantityText(row.usedQuantity);
            card.counters.get('Market accounted').textContent = quantityText(row.marketSpentQuantity);
            for (const source of card.sources.values()) {
                const used = stockValues(row[source.definition.used]);
                const usedTotal = stockTotal(used);
                source.usageTotal.textContent = usedTotal === null ? '—' : quantityText(usedTotal);
                source.usageCounters.forEach((quantity, kind) => { quantity.textContent = quantityText(used[kind]); });
                const key = stockSourceKey(row.itemId, source.source);
                const draft = stockEditorDrafts.get(key);
                const stored = stockValues(row[source.definition.stock]);
                const values = draft && draft.dirty ? draft.values : stored;
                source.inputs.forEach((input, kind) => {
                    const next = values[kind];
                    const value = draft && draft.dirty ? String(next ?? '') : validStockQuantity(next) ? String(next) : '';
                    if (input.value !== value) input.value = value;
                });
                const storedTotal = stockTotal(stored);
                source.fieldset.classList.toggle('hidden', source.source === 'market-fallback' ? !(storedTotal > 0) && !(usedTotal > 0) :
                    source.source === 'market' && row.marketTracked !== true && !(usedTotal > 0));
                if (source.note) source.note.classList.toggle('hidden', !(storedTotal > 0));
            }
            card.error.textContent = safeText(row.editError);
            card.error.classList.toggle('hidden', !card.error.textContent);
            const children = $('stock-editor-rows').children;
            if (children[index * 2] !== card.element) $('stock-editor-rows').insertBefore(card.element, children[index * 2] || null);
            if (children[index * 2 + 1] !== card.detail) $('stock-editor-rows').insertBefore(card.detail, children[index * 2 + 1] || null);
        });
        const error = stockEditorUIError || safeText(editor && editor.error);
        const warning = stockEditorStale ? 'Stock changed. Refresh the list before saving.' : safeText(editor && editor.warning);
        $('stock-editor-status').textContent = editor && editor.loading ? 'Loading items…' : editor && editor.saving ? 'Saving stock…' : safeText(editor && editor.status);
        $('stock-editor-status').classList.toggle('hidden', !$('stock-editor-status').textContent);
        $('stock-editor-warning').textContent = warning;
        $('stock-editor-warning').classList.toggle('hidden', !warning);
        $('stock-editor-error').textContent = error;
        $('stock-editor-error').classList.toggle('hidden', !error);
        filterStockEditor();
        updateStockEditorControls();
        if (focused && focused.isConnected && document.activeElement !== focused && focused.closest('#stock-editor-rows') && !focused.disabled) focused.focus({ preventScroll: true });
    };
    const stockEditorAction = async (method, args = []) => {
        if (stockEditorProcessing() || localBusy || state.busy || handoverProcessing() || settingsOperationBusy() || pendingOptions.size) return;
        const toggle = method === 'OpenStockEditor' || method === 'CloseStockEditor';
        if (handoverBlocksAll() || !toggle && (!state.hasAPIKey || !state.characterUid)) return;
        if (method === 'SaveStockEditorItem' && args[0] && (stockEditorPendingBlocksItem(args[0].itemId) || handoverBlocksItem(args[0].itemId))) return;
        if (method === 'DiscardStockEditorEdit' && stockEditorState() && stockEditorState().background === true) return;
        const context = stockEditorContext();
        const revision = ++stockEditorRequestRevision;
        stockEditorRequest = method;
        stockEditorUIError = '';
        renderStockEditor();
        updateControls();
        try {
            const result = await call(method, ...args);
            if (context === stockEditorContext() && revision === stockEditorRequestRevision) {
                const editorResult = result && result.stockEditor;
                const sequence = result && result.stateSequence;
                stockEditorAcceptGeneration = !!editorResult && !safeText(editorResult.error) &&
                    (!Number.isSafeInteger(sequence) ? latestStateSequence === null : latestStateSequence === null || sequence >= latestStateSequence);
                try { await refresh(result); } finally { stockEditorAcceptGeneration = false; }
                const editor = stockEditorState();
                if (method === 'CloseStockEditor' && editor && editor.open &&
                    (editor.loading || editor.saving || state.localBackup && state.localBackup.busy)) requestStockEditorClose();
            }
        } catch (_) {
            if (context === stockEditorContext() && revision === stockEditorRequestRevision) stockEditorUIError = 'Stock could not be updated. Please try again.';
        } finally {
            if (revision === stockEditorRequestRevision) {
                stockEditorRequest = '';
                stockEditorRequestSource = '';
                renderStockEditor();
                renderTracking();
                updateControls();
                if (!$('settings').open && method !== 'CloseStockEditor' && stockEditorState() && stockEditorState().open) requestStockEditorClose();
            }
        }
    };
    const localBackupAction = async (method) => {
        if (!['ImportLocalProgress', 'ExportLocalProgress'].includes(method) || (!nativeApp() && !preview) || isBusy() || pendingOptions.size) return;
        const context = stockEditorContext();
        const revision = ++localBackupRequestRevision;
        localBackupRequest = method;
        localBackupUIError = '';
        renderLocalBackup();
        updateControls();
        try {
            const result = await call(method);
            if (context === stockEditorContext() && revision === localBackupRequestRevision) await refresh(result);
        } catch (_) {
            if (context === stockEditorContext() && revision === localBackupRequestRevision) localBackupUIError =
                method === 'ImportLocalProgress' ? 'Local Progress could not be imported. Please try again.' : 'Local Progress could not be exported. Please try again.';
        } finally {
            if (revision === localBackupRequestRevision) {
                localBackupRequest = '';
                renderLocalBackup();
                renderStockEditor();
                renderTracking();
                updateControls();
                if (!$('settings').open && stockEditorState() && stockEditorState().open) requestStockEditorClose();
            }
        }
    };
    const retryMarketUpload = async () => {
        if ((!nativeApp() && !preview) || state.marketFallbackPending !== true || !state.hasAPIKey || !state.characterUid || isBusy() || marketUploadRetryBlocked() || pendingOptions.size) return;
        const context = stockEditorContext();
        const revision = ++marketUploadRetryRevision;
        marketUploadRetryPending = true;
        stockEditorUIError = '';
        renderStockEditor();
        updateControls();
        try {
            const result = await call('RetryMarketUpload');
            if (context === stockEditorContext() && revision === marketUploadRetryRevision) await refresh(result);
        } catch (_) {
            if (context === stockEditorContext() && revision === marketUploadRetryRevision) stockEditorUIError = 'The saved Market upload could not be retried. Please try again.';
        } finally {
            if (revision === marketUploadRetryRevision) {
                marketUploadRetryPending = false;
                renderStockEditor();
                renderTracking();
                updateControls();
                if (!$('settings').open && stockEditorState() && stockEditorState().open) requestStockEditorClose();
            }
        }
    };
    const saveStockEditorSource = (itemId, sourceName, clear = false) => {
        const editor = stockEditorState();
        const card = stockEditorCards.get(itemId);
        const source = card && card.sources.get(sourceName);
        if (!source || (clear ? source.clear : source.save).disabled || !stockEditorMatchesContext(editor) || !stockEditorSnapshotIdentity(editor) || stockEditorStale ||
            stockEditorSnapshotIdentity(editor) !== stockEditorIdentity) return;
        const values = clear ? { fir: 0, regular: 0, unknown: 0 } : stockSourceValues(source);
        if (stockTotal(values) === null) return;
        stockEditorRequestSource = stockSourceKey(itemId, sourceName);
        stockEditorAction('SaveStockEditorItem', [{ generation: editor.generation, itemId, source: sourceName, ...values }]);
    };

    const validateHandoverQuantity = (id, input) => {
        const valid = validHandoverQuantity(input.valueAsNumber);
        input.setAttribute('aria-invalid', String(!valid));
        if (valid) invalidHandoverQuantities.delete(id);
        else invalidHandoverQuantities.add(id);
        return valid;
    };

    const updateHandoverRow = (id, row) => {
        const quantity = row.querySelector('[data-handover="quantity"]');
        handoverEdits.set(id, { quantity: quantity.value, kind: row.querySelector('[data-handover="kind"]').value });
        if (!validateHandoverQuantity(id, quantity)) {
            handoverUIError = 'Enter a positive whole quantity.';
            renderHandover();
            updateControls();
            return;
        }
        handoverAction('UpdateHandoverItem', [id, quantity.valueAsNumber, row.querySelector('[data-handover="kind"]').value]);
    };

    const createHandoverRow = (id) => {
        const row = document.createElement('li');
        row.className = 'handover-item';
        const heading = document.createElement('div');
        heading.className = 'handover-item-heading';
        const name = document.createElement('span');
        name.className = 'handover-item-name';
        name.dataset.handover = 'name';
        const remove = document.createElement('button');
        remove.type = 'button';
        remove.className = 'handover-remove';
        remove.dataset.handover = 'remove';
        remove.innerHTML = '<svg viewBox="0 0 18 18" aria-hidden="true"><path d="M3 4h12M6 4V2h6v2M5 4v12h8V4M8 7v6M10 7v6" /></svg>';
        remove.addEventListener('click', () => handoverAction('RemoveHandoverItem', [id]));
        heading.append(name, remove);
        const fields = document.createElement('div');
        fields.className = 'handover-item-fields';
        const quantityField = document.createElement('div');
        const quantityLabel = document.createElement('label');
        const quantity = document.createElement('input');
        quantity.id = 'handover-quantity-' + (++handoverFieldID);
        quantity.type = 'number';
        quantity.min = '1';
        quantity.max = '1000000000';
        quantity.step = '1';
        quantity.inputMode = 'numeric';
        quantity.required = true;
        quantity.dataset.handover = 'quantity';
        quantityLabel.htmlFor = quantity.id;
        quantityLabel.textContent = 'Quantity';
        const quantityControls = document.createElement('div');
        quantityControls.className = 'handover-quantity';
        const minus = document.createElement('button');
        const plus = document.createElement('button');
        for (const [button, delta, label] of [[minus, -1, 'Decrease quantity'], [plus, 1, 'Increase quantity']]) {
            button.type = 'button';
            button.textContent = delta < 0 ? '−' : '+';
            button.dataset.handover = delta < 0 ? 'minus' : 'plus';
            button.setAttribute('aria-label', label);
            button.addEventListener('click', () => {
                const current = handoverItems().find((item) => item.id === id);
                const value = validHandoverQuantity(quantity.valueAsNumber) ? quantity.valueAsNumber : current && current.quantity || 1;
                quantity.value = String(Math.max(1, Math.min(1000000000, value + delta)));
                updateHandoverRow(id, row);
            });
        }
        quantity.addEventListener('input', () => {
            handoverEdits.set(id, { quantity: quantity.value, kind: kind.value });
            validateHandoverQuantity(id, quantity);
            if (!invalidHandoverQuantities.size) handoverUIError = '';
            renderHandover();
            updateControls();
        });
        quantity.addEventListener('change', () => updateHandoverRow(id, row));
        quantityControls.append(minus, quantity, plus);
        quantityField.append(quantityLabel, quantityControls);
        const kindField = document.createElement('div');
        const kindLabel = document.createElement('label');
        const kind = document.createElement('select');
        kind.id = 'handover-kind-' + handoverFieldID;
        kind.dataset.handover = 'kind';
        kindLabel.htmlFor = kind.id;
        kindLabel.textContent = 'Type';
        for (const [value, label] of [['regular', 'Regular'], ['fir', 'Found in Raid']]) {
            const option = document.createElement('option');
            option.value = value;
            option.textContent = label;
            kind.append(option);
        }
        kind.addEventListener('change', () => updateHandoverRow(id, row));
        const split = document.createElement('button');
        split.type = 'button';
        split.className = 'handover-split';
        split.dataset.handover = 'split';
        split.textContent = 'Add other type';
        split.addEventListener('click', () => handoverAction('SplitHandoverItem', [id]));
        kindField.append(kindLabel, kind, split);
        fields.append(quantityField, kindField);
        row.append(heading, fields);
        return row;
    };

    const updateHandoverControls = () => {
        const handover = state.handover;
        const processing = handoverProcessing();
        const progressLoading = handoverProgressLoading();
        const frozen = isBusy() || !!(handover && (handover.pending || handover.completed));
        $('btn-handover').classList.toggle('hidden', !state.hasAPIKey || !state.characterUid);
        $('btn-handover').disabled = (!nativeApp() && !preview) || isBusy() || stockEditorBlocksStock() || !!(handover && handover.open) || pendingOptions.size > 0;
        for (const [id, row] of handoverRows) {
            row.querySelectorAll('input, select, button').forEach((control) => { control.disabled = frozen; });
            row.querySelector('[data-handover="minus"]').disabled = frozen || row.querySelector('[data-handover="quantity"]').valueAsNumber <= 1;
            row.querySelector('[data-handover="plus"]').disabled = frozen || row.querySelector('[data-handover="quantity"]').valueAsNumber >= 1000000000;
            const item = handoverItems().find((entry) => entry.id === id);
            row.querySelector('[data-handover="split"]').disabled = frozen || !!item && handoverItems().filter((entry) => entry.itemId === item.itemId).length >= 2;
        }
        $('btn-handover-cancel').disabled = processing || settingsOperationBusy();
        $('handover-search-name').disabled = frozen;
        $('btn-handover-search').disabled = frozen || !$('handover-search-name').value.trim() || !state.hasAPIKey;
        $('btn-handover-scan').disabled = frozen || !state.hasAPIKey;
        $('btn-handover-scan').querySelector('span').textContent = isBusy() ? 'Processing…' : 'Scan now';
        $('handover-automatic').disabled = frozen;
        $('handover-automatic').checked = !!(handover && handover.automatic);
        $('btn-handover-cancel').textContent = handover && handover.completed ? 'Done' : handover && handover.pending ? 'Close' : 'Cancel';
        $('btn-handover-confirm').classList.toggle('hidden', !!(handover && (handover.completed || handover.background === true)));
        $('btn-handover-confirm').textContent = processing || progressLoading ? 'Processing…' : handover && handover.pending ? 'Resume hand-in' : 'Confirm hand-in';
        $('btn-handover-confirm').disabled = isBusy() || pendingOptions.size > 0 || progressLoading || marketFallbackBlocksAll() || stockEditorPendingBlocksAll() ||
            handoverItems().some((item) => stockEditorPendingBlocksItem(item.itemId)) || !handover || !handover.open || !!handover.completed || handover.background === true ||
            invalidHandoverQuantities.size > 0 || (!handover.pending && !handoverItems().length);
        $('handover-form').setAttribute('aria-busy', String(isBusy()));
    };

    const renderHandover = () => {
        const handover = state.handover;
        const dialog = $('handover-dialog');
        const open = !!(handover && handover.open);
        const items = handoverItems();
        if (!open && !(handover && handover.pending)) handoverEdits.clear();
        if (handover && (handover.pending || handover.completed)) handoverEdits.clear();
        const ids = new Set(items.map((item) => item.id));
        for (const [id, row] of handoverRows) {
            if (!ids.has(id)) {
                row.remove();
                handoverRows.delete(id);
                handoverEdits.delete(id);
                invalidHandoverQuantities.delete(id);
            }
        }
        items.forEach((item, index) => {
            let row = handoverRows.get(item.id);
            if (!row) {
                row = createHandoverRow(item.id);
                handoverRows.set(item.id, row);
            }
            const name = safeText(item.name) || 'Unknown item';
            row.querySelector('[data-handover="name"]').textContent = name;
            row.querySelector('[data-handover="remove"]').setAttribute('aria-label', 'Remove ' + name);
            row.querySelector('[data-handover="split"]').setAttribute('aria-label', 'Add other type for ' + name);
            const quantity = row.querySelector('[data-handover="quantity"]');
            let edit = handoverEdits.get(item.id);
            if (edit && Number(edit.quantity) === item.quantity && edit.kind === item.kind) {
                handoverEdits.delete(item.id);
                edit = null;
            }
            if (document.activeElement !== quantity) {
                quantity.value = edit ? edit.quantity : String(item.quantity);
                validateHandoverQuantity(item.id, quantity);
            }
            const kind = row.querySelector('[data-handover="kind"]');
            if (document.activeElement !== kind) kind.value = edit ? edit.kind : item.kind === 'fir' ? 'fir' : 'regular';
            if ($('handover-items').children[index] !== row) $('handover-items').insertBefore(row, $('handover-items').children[index] || null);
        });
        $('handover-empty').classList.toggle('hidden', !!items.length || !!(handover && handover.completed));
        const status = state.busy ? statusNames[state.status] || safeText(state.status) :
            handoverProcessing() ? 'Processing hand-in…' : safeText(handover && handover.status);
        const error = handoverUIError || safeText(handover && handover.error);
        $('handover-status').textContent = status;
        $('handover-status').classList.toggle('hidden', !status);
        $('handover-status').classList.toggle('completed', !!(handover && handover.completed));
        $('handover-error').textContent = error;
        $('handover-error').classList.toggle('hidden', !error);
        updateHandoverControls();
        if (open && !dialog.open) {
            handoverReturnFocus = document.activeElement;
            dialog.showModal();
        } else if (!open && dialog.open) {
            dialog.close();
            handoverUIError = '';
        }
        if (!open && !handoverRequestPending && handoverReturnFocus) {
            if (handoverReturnFocus.isConnected && !handoverReturnFocus.disabled) handoverReturnFocus.focus();
            handoverReturnFocus = null;
        }
        if (open && !isBusy() && document.activeElement === dialog && !$('handover-search-name').disabled) {
            $('handover-search-name').focus();
        }
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

    const scheduleStockEditorClose = () => {
        if ($('settings').open) {
            stockEditorCloseRequested = false;
            return;
        }
        if (!stockEditorCloseRequested || stockEditorCloseScheduled) return;
        const editor = stockEditorState();
        if (!editor || !editor.open) {
            if (!stockEditorProcessing()) stockEditorCloseRequested = false;
            return;
        }
        if (isBusy() || pendingOptions.size || handoverBlocksAll()) return;
        const context = stockEditorContext();
        const sequence = latestStateSequence;
        stockEditorCloseScheduled = true;
        queueMicrotask(() => {
            stockEditorCloseScheduled = false;
            if ($('settings').open || context !== stockEditorContext()) {
                stockEditorCloseRequested = false;
                return;
            }
            if (!stockEditorCloseRequested) return;
            if (sequence !== latestStateSequence) {
                scheduleStockEditorClose();
                return;
            }
            if (isBusy() || pendingOptions.size || handoverBlocksAll()) return;
            stockEditorCloseRequested = false;
            if (stockEditorState() && stockEditorState().open) stockEditorAction('CloseStockEditor');
        });
    };
    const requestStockEditorClose = () => {
        if ($('settings').open) return;
        stockEditorCloseRequested = true;
        scheduleStockEditorClose();
    };

    const updateControls = () => {
        const available = !!nativeApp() || preview;
        const busy = isBusy();
        const settingsBusy = settingsOperationBusy();
        $('btn-import-local').disabled = !available || busy || pendingOptions.size > 0;
        $('btn-export-local').disabled = !available || busy || pendingOptions.size > 0;
        $('search-name').disabled = !available || settingsBusy;
        $('btn-search').disabled = !available || busy || !state.hasAPIKey || !$('search-name').value.trim();
        $('btn-scan').disabled = !available || busy || !state.hasAPIKey || !englishModelAvailable();
        $('btn-save-key').disabled = !available || busy || keyFromStorage || !$('api-key').value.trim();
        $('btn-clear-key').disabled = !available || busy || !state.hasAPIKey;
        $('btn-toggle-key').disabled = !available || busy || !state.hasAPIKey && !$('api-key').value;
        $('api-key').disabled = !available || busy;
        updateKeyVisibility();
        $('btn-item-page').disabled = !available || busy || !state.item || !state.item.uid;
        $('scan-automatic').disabled = !available || busy;
        $('btn-refresh-local').disabled = !available || localBusy || settingsBusy || !state.hasAPIKey || !state.characterUid || !!(state.local && state.local.loading);
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
            panel.querySelector('[data-tracked="add-fir"]').disabled = !canAddTracking(entries[index], 'fir');
            const badge = panel.querySelector('[data-tracked="add-hotkey"]');
            badge.textContent = hotkeys[0].value;
            badge.classList.toggle('hidden', !hotkeys[0].ready);
            const firBadge = panel.querySelector('[data-tracked="add-fir-hotkey"]');
            firBadge.textContent = hotkeys[1].value;
            firBadge.classList.toggle('hidden', !hotkeys[1].ready);
        });
        $('btn-min').disabled = !window.runtime || !window.runtime.WindowMinimise;
        $('btn-close').disabled = !nativeApp();
        $('btn-scan').querySelector('span').textContent = busy ? 'Processing…' : 'Scan now';
        updateHandoverControls();
        updateStockEditorControls();
        scheduleStockEditorClose();
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
        state = { ...state, ...nextState, localProgress: true,
            marketFallbackItemId: safeText(nextState.marketFallbackItemId).trim(), marketFallbackInvalid: nextState.marketFallbackInvalid === true,
            marketFallbackItemIds: nextState.marketFallbackItemIds,
            marketFallbackAppendAllowed: nextState.marketFallbackAppendAllowed === true,
            marketFallbackAppendAllowedItemIds: nextState.marketFallbackAppendAllowedItemIds,
            progressSync: nextState.progressSync && typeof nextState.progressSync === 'object' ? nextState.progressSync : null };
        if (!state.hasAPIKey && (keyFromStorage || keyRevealPending)) resetKeyDisplay(true);
        const trackedState = currentTracking();
        if (trackingFailure && (!sameTrackingTarget(trackingFailure) || trackedState && (trackedState.loading || trackedState.adding))) {
            trackingFailure = null;
        }
        const item = state.item;
        const marketAvailable = !!item && item.haveMarketData === true && !item.bannedOnFlea;
        const slots = item && Number.isInteger(item.slots) && item.slots > 0 ? item.slots : 0;
        $('item-name').textContent = item ? safeText(item.name) || safeText(item.shortName) || 'Unknown item' : 'Waiting for item';
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
        renderProgressSync();
        renderLocalBackup();
        renderHandover();
        renderStockEditor();

        $('key-indicator').textContent = state.hasAPIKey ? 'API key saved' : 'API key required';
        $('key-indicator').classList.toggle('saved', !!state.hasAPIKey);
        $('api-key').placeholder = state.hasAPIKey ? '••••••••••••••••' : 'Enter API key';
        $('mode-badge').textContent = String(state.mode || 'PVP').toUpperCase();
        const status = safeText(state.status);
        const automaticEnabled = !!state.enabled && !!state.automatic;
        const waitingStatus = ['', 'idle', 'ready', 'waiting', 'disabled', 'Waiting for an item Inspect window.',
            'Waiting for the game', 'Automatic scanning is off.'].includes(status);
        $('status-text').textContent = !state.busy && progressSyncMessage() ? progressSyncMessage() :
            state.hasAPIKey && !automaticEnabled && waitingStatus ? statusNames.disabled :
                statusNames[status] || status || (state.hasAPIKey ? 'Waiting for an item Inspect window.' : 'API key required');
        $('status-dot').className = 'status-dot' + (state.error || safeText(progressSyncState() && progressSyncState().error) ? ' error' :
            isBusy() || progressSyncState() && progressSyncState().running ? ' busy' : state.hasAPIKey && automaticEnabled ? ' ready' : '');
        $('error-text').textContent = safeText(state.error);
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
            $('settings').open = !state.hasAPIKey || !!(state.stockEditor && state.stockEditor.open);
            initialRender = false;
        }
        updateControls();
    };

    const previewVariants = preview && new URLSearchParams(window.location.search).get('variants') === '1';
    const previewCompletion = preview && new URLSearchParams(window.location.search).get('completion') === '1';
    const sampleItem = previewVariants ? {
        uid: 'preview-fuel-tank', bsgId: 'preview-tracked-item', name: 'Metal fuel tank', shortName: 'Fuel',
        price: 118000, avg24hPrice: 121600, traderName: 'Therapist', traderPrice: 28920, traderPriceCur: '₽', slots: 6,
        haveMarketData: true, bannedOnFlea: false,
    } : {
        uid: 'preview-item', bsgId: 'preview-tracked-item', name: 'Galvion Caiman Hybrid helmet (MultiCam Alpine)', shortName: 'Caiman',
        price: 162000, avg24hPrice: 154800, traderName: 'Ragman', traderPrice: 25660, traderPriceCur: '₽', slots: 4,
        haveMarketData: true, bannedOnFlea: false,
    };
    let previewState = {
        ...state, hasAPIKey: true, addHotkey: 'F2', addHotkeyReady: true, firHotkey: 'F1', firHotkeyReady: true, status: 'Item recognized.', ocrLanguages: ['en'], cachedAt: '2026-10-10T22:30:23Z', item: sampleItem,
        characterUid: 'preview-character', characters: [{ charUid: 'preview-character', name: 'Sample character', gameMode: 'PVP' }],
        tracking: { itemUid: sampleItem.uid, itemId: 'preview-tracked-item', charUid: 'preview-character',
            tracked: true, loading: false, adding: false, availableQuantity: 0, requiredQuantity: 7,
            remainingQuantity: 7, requirement: 'fir', requirements: [{ kind: 'fir', requiredQuantity: 7 }], error: '', status: '' },
    };
    if (previewVariants) {
        const variantError = 'Tarkov Market API cannot update variants that share the same item ID separately.';
        const full = { ...previewState.tracking, variantName: 'Metal fuel tank (100/100)', availableQuantity: 1,
            requiredQuantity: 4, remainingQuantity: 3, requirement: 'fir', requirements: [{ kind: 'fir', requiredQuantity: 4 }], error: variantError };
        const empty = { ...previewState.tracking, variantName: 'Metal fuel tank (0/100)', availableQuantity: 2,
            requiredQuantity: 6, remainingQuantity: 4, requirement: 'regular', requirements: [{ kind: 'regular', requiredQuantity: 6 }], error: variantError };
        previewState.tracking = { ...full, variants: [full, empty] };
    }
    if (preview) {
        previewState.item = { ...sampleItem, name: 'Salewa first aid kit', shortName: 'Salewa', slots: 2,
            price: 0, avg24hPrice: 0, traderPrice: 0, traderName: '', haveMarketData: false };
        const storage = new URLSearchParams(window.location.search).get('localOnly') === '1' ? 'local' : 'market';
        previewState.tracking = { ...previewState.tracking, local: true, storage, requirement: 'mixed',
            availableQuantity: previewCompletion ? 0 : 2, requiredQuantity: previewCompletion ? 7 : 8, remainingQuantity: previewCompletion ? 7 : 6,
            marketTracked: storage === 'market', marketRequiredQuantity: storage === 'market' ? 3 : 0,
            marketAvailableQuantity: storage === 'market' && !previewCompletion ? 2 : 0,
            localAvailableQuantity: storage === 'local' && !previewCompletion ? 2 : 0,
            previewFIRStock: 0, previewRegularStock: previewCompletion ? 0 : 2, hasLocalRequirements: true,
            addRegularAllowed: true, addFIRAllowed: true,
            requirements: [{ kind: 'fir', requiredQuantity: previewCompletion ? 2 : 5, availableQuantity: 0 },
                { kind: 'regular', requiredQuantity: previewCompletion ? 5 : 3, availableQuantity: previewCompletion ? 0 : 2 }] };
        previewState.local = { loading: false, error: '', warning: '', updatedAt: '2026-10-11T21:30:00Z' };
    }
    let previewAPIKey = 'preview-api-key';
    const call = async (method, ...args) => {
        const app = nativeApp();
        if (app && typeof app[method] === 'function') return app[method](...args);
        if (!preview) throw new Error('The app connection is unavailable.');
        if (method === 'RevealAPIKey') return previewState.hasAPIKey ? previewAPIKey : '';
        if (method === 'OpenHandover') {
            const previous = previewState.handover;
            previewState.handover = previous && previous.pending ? { ...previous, open: true } : {
                open: true, busy: false, pending: false, completed: false, automatic: true, error: '', status: '',
                items: [],
            };
        }
        if (method === 'CloseHandover' && previewState.handover) previewState.handover = { ...previewState.handover, open: false };
        if (method === 'SetHandoverAutomatic' && previewState.handover) previewState.handover = { ...previewState.handover, automatic: !!args[0] };
        if (method === 'UpdateHandoverItem' && previewState.handover && validHandoverQuantity(args[1]) && ['regular', 'fir'].includes(args[2])) {
            previewState.handover = { ...previewState.handover, items: previewState.handover.items.map((item) =>
                item.id === args[0] ? { ...item, quantity: args[1], kind: args[2] } : item) };
        }
        if (method === 'RemoveHandoverItem' && previewState.handover) previewState.handover = { ...previewState.handover,
            items: previewState.handover.items.filter((item) => item.id !== args[0]) };
        if (method === 'SplitHandoverItem' && previewState.handover) {
            const item = previewState.handover.items.find((entry) => entry.id === args[0]);
            if (item && previewState.handover.items.filter((entry) => entry.itemId === item.itemId).length < 2) {
                previewState.handover = { ...previewState.handover, items: [...previewState.handover.items,
                    { ...item, id: item.id + '-other', quantity: 1, kind: item.kind === 'fir' ? 'regular' : 'fir' }] };
            }
        }
        if (method === 'ConfirmHandover' && previewState.handover) previewState.handover = { ...previewState.handover,
            completed: true, pending: false, busy: false, error: '', status: 'Hand-in recorded.' };
        if (method === 'SetOptions') {
            const addHotkey = normalizedHotkey(args[0].addHotkey, 'F2');
            const firHotkey = normalizedHotkey(args[0].firHotkey, 'F1');
            if (addHotkey !== 'off' && addHotkey === firHotkey) throw new Error('Duplicate shortcut.');
            previewState = { ...previewState, ...args[0], addHotkey, firHotkey,
                addHotkeyReady: addHotkey !== 'off' && !previewState.hotkeyCapture, firHotkeyReady: firHotkey !== 'off' && !previewState.hotkeyCapture,
                addHotkeyError: '', firHotkeyError: '' };
            if (previewState.tracking) previewState.tracking = { ...previewState.tracking, local: true };
        }
        if (method === 'SetHotkeyCapture') previewState = { ...previewState, hotkeyCapture: !!args[0],
            addHotkeyReady: !args[0] && previewState.addHotkey !== 'off', firHotkeyReady: !args[0] && previewState.firHotkey !== 'off' };
        if (method === 'RefreshLocalProgress' && previewState.local) {
            const requiredQuantity = previewCompletion ? 4 : 5;
            const firRequired = previewCompletion ? 1 : 2;
            const firStock = previewState.tracking.previewFIRStock || 0;
            const regularStock = previewState.tracking.previewRegularStock || 0;
            const requirements = [{ kind: 'fir', requiredQuantity: firRequired, availableQuantity: Math.min(firStock, firRequired) },
                { kind: 'regular', requiredQuantity: 3, availableQuantity: Math.min(3, regularStock + Math.max(0, firStock - firRequired)) }];
            const addRegularAllowed = requirements[1].availableQuantity < requirements[1].requiredQuantity;
            const addFIRAllowed = addRegularAllowed || requirements[0].availableQuantity < requirements[0].requiredQuantity;
            previewState.tracking = { ...previewState.tracking, requiredQuantity, marketRequiredQuantity: 0, storage: 'local',
                remainingQuantity: Math.max(0, requiredQuantity - previewState.tracking.availableQuantity),
                requirements, addRegularAllowed, addFIRAllowed,
                completedSources: [{ type: 'quest', quantity: 3, optional: false }] };
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
            if (previewState.handover && previewState.handover.open &&
                !previewState.handover.items.some((entry) => entry.itemId === item.bsgId)) {
                previewState.handover = { ...previewState.handover, items: [...previewState.handover.items,
                    { id: item.bsgId + '-regular', itemId: item.bsgId, name: item.name, quantity: 1, kind: 'regular' }] };
            }
        }
        if ((method === 'AddTrackedItem' || method === 'AddTrackedItemFIR') && previewState.tracking && !previewState.tracking.error &&
            !previewState.tracking.variants && args[0] === previewState.tracking.itemUid) {
            const tracking = previewState.tracking;
            const destination =
                !(tracking.marketTracked === true && tracking.marketAvailableQuantity < tracking.marketRequiredQuantity) ? 'local' : 'market';
            const marketAvailableQuantity = (tracking.marketAvailableQuantity || 0) + (destination === 'market' ? 1 : 0);
            const localAvailableQuantity = (tracking.localAvailableQuantity || 0) + (destination === 'local' ? 1 : 0);
            const availableQuantity = marketAvailableQuantity + localAvailableQuantity;
            const storage = tracking.marketTracked === true && marketAvailableQuantity < tracking.marketRequiredQuantity ? 'market' : 'local';
            const previewFIRStock = (tracking.previewFIRStock || 0) + (method === 'AddTrackedItemFIR' ? 1 : 0);
            const previewRegularStock = (tracking.previewRegularStock || 0) + (method === 'AddTrackedItem' ? 1 : 0);
            const firRequired = (tracking.requirements || []).filter((row) => row.kind === 'fir').reduce((sum, row) => sum + row.requiredQuantity, 0);
            const requirements = (tracking.requirements || []).map((row) => ({ ...row,
                availableQuantity: row.kind === 'fir' ? Math.min(previewFIRStock, firRequired) :
                    Math.min(row.requiredQuantity, previewRegularStock + Math.max(0, previewFIRStock - firRequired)) }));
            const addRegularAllowed = requirements.some((row) => row.kind === 'regular' && row.availableQuantity < row.requiredQuantity);
            const addFIRAllowed = addRegularAllowed || requirements.some((row) => row.kind === 'fir' && row.availableQuantity < row.requiredQuantity);
            previewState = { ...previewState, tracking: { ...tracking, availableQuantity,
                marketAvailableQuantity, localAvailableQuantity, storage,
                previewFIRStock, previewRegularStock, requirements, addRegularAllowed, addFIRAllowed,
                remainingQuantity: Math.max(0, tracking.requiredQuantity - availableQuantity),
                status: 'Added 1 item to Tracked Items stock.' } };
        }
        return { ...previewState };
    };
    const refresh = async (result) => {
        render(result && typeof result === 'object' ? result : await call('GetState'));
    };
    const action = async (method, args = [], success = '') => {
        if (isBusy()) return false;
        const context = stockEditorContext();
        localBusy = true;
        showFeedback('');
        updateControls();
        try {
            const result = await call(method, ...args);
            await refresh(result);
            if (method === 'RefreshLocalProgress' && context === stockEditorContext() && trackingFailure &&
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

    const handoverAction = async (method, args = []) => {
        const handover = state.handover;
        if (handoverProcessing() || settingsOperationBusy()) return;
        if (method === 'OpenHandover' && (!state.hasAPIKey || !state.characterUid || stockEditorBlocksStock())) return;
        if (method !== 'OpenHandover' && (!handover || !handover.open)) return;
        if (['UpdateHandoverItem', 'RemoveHandoverItem', 'SplitHandoverItem', 'SetHandoverAutomatic', 'ScanNow', 'SearchItem'].includes(method) &&
            (isBusy() || handover.pending || handover.completed)) return;
        if (method === 'ConfirmHandover' && (isBusy() || pendingOptions.size > 0 || handoverProgressLoading() || marketFallbackBlocksAll() || stockEditorPendingBlocksAll() ||
            handoverItems().some((item) => stockEditorPendingBlocksItem(item.itemId)) || handover.completed || handover.background === true ||
            invalidHandoverQuantities.size || !handover.pending && !handoverItems().length)) return;
        handoverRequestPending = true;
        handoverUIError = '';
        renderHandover();
        updateControls();
        try {
            if (method === 'ConfirmHandover' && !handover.pending) {
                for (const [id, edit] of [...handoverEdits]) {
                    const quantity = Number(edit.quantity);
                    if (!validHandoverQuantity(quantity)) throw new Error('Invalid hand-in quantity.');
                    await refresh(await call('UpdateHandoverItem', id, quantity, edit.kind));
                    if (handoverEdits.has(id)) throw new Error('The hand-in edit was not saved.');
                }
            }
            await refresh(await call(method, ...args));
        } catch (_) {
            handoverUIError = 'The hand-in could not be updated. Please try again.';
            if (!state.handover || !state.handover.open) showFeedback(handoverUIError, true);
        } finally {
            handoverRequestPending = false;
            renderHandover();
            renderTracking();
            updateControls();
        }
    };

    const readOptions = () => ({
        enabled: true, automatic: $('scan-automatic').checked,
        addHotkey: normalizedHotkey($('add-hotkey').value, 'F2'),
        firHotkey: normalizedHotkey($('fir-hotkey').value, 'F1'),
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
        if (input && settingsOperationBusy()) return Promise.resolve();
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
        if (settingsOperationBusy()) return;
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
            shortcutErrors.set(field.id, 'Choose a different shortcut for each item type.');
            updateControls();
            return;
        }
        shortcutErrors.delete(field.id);
        input.value = value;
        saveOptions({ target: input });
    };

    const addTrackedItem = async (kind = 'regular') => {
        const tracking = currentTracking();
        if (!canAddTracking(tracking, kind)) return;
        const target = { itemUid: state.item.uid, charUid: state.characterUid };
        const context = stockEditorContext();
        addingTracking = target;
        trackingFailure = null;
        localBusy = true;
        renderTracking();
        updateControls();
        try {
            const result = await call(kind === 'fir' ? 'AddTrackedItemFIR' : 'AddTrackedItem', target.itemUid);
            if (sameTrackingTarget(target) && context === stockEditorContext()) await refresh(result);
        } catch (_) {
            if (sameTrackingTarget(target) && context === stockEditorContext()) trackingFailure = { ...target, stateSequence: latestStateSequence, message: 'The item could not be added. Please try again.' };
        } finally {
            addingTracking = null;
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
        $('btn-handover').addEventListener('click', () => handoverAction('OpenHandover'));
        $('btn-handover-cancel').addEventListener('click', () => handoverAction('CloseHandover'));
        const searchHandoverItem = () => {
            const name = $('handover-search-name').value.trim();
            if (name && state.hasAPIKey) handoverAction('SearchItem', [name]);
        };
        $('btn-handover-search').addEventListener('click', searchHandoverItem);
        $('handover-search-name').addEventListener('input', updateHandoverControls);
        $('handover-search-name').addEventListener('keydown', (event) => {
            if (event.key === 'Enter') {
                event.preventDefault();
                searchHandoverItem();
            }
        });
        $('btn-handover-scan').addEventListener('click', () => handoverAction('ScanNow'));
        $('handover-automatic').addEventListener('change', () => handoverAction('SetHandoverAutomatic', [$('handover-automatic').checked]));
        $('handover-form').addEventListener('submit', (event) => {
            event.preventDefault();
            handoverAction('ConfirmHandover');
        });
        $('handover-dialog').addEventListener('cancel', (event) => {
            event.preventDefault();
            if (!handoverProcessing()) handoverAction('CloseHandover');
        });
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
                if (stockEditorState() && stockEditorState().open || stockEditorRequest && stockEditorRequest !== 'CloseStockEditor') requestStockEditorClose();
                updateControls();
            } else stockEditorCloseRequested = false;
        });
        const stockEditorSummary = $('stock-editor').querySelector('summary');
        const guardStockEditorToggle = (event) => {
            if ((!nativeApp() && !preview) || isBusy() || pendingOptions.size || handoverBlocksAll()) event.preventDefault();
        };
        stockEditorSummary.addEventListener('click', guardStockEditorToggle);
        stockEditorSummary.addEventListener('keydown', (event) => {
            if (event.key === 'Enter' || event.key === ' ') guardStockEditorToggle(event);
        });
        $('stock-editor').addEventListener('toggle', () => {
            if (stockEditorRequest) return;
            const editor = stockEditorState();
            if ($('stock-editor').open && !(editor && editor.open)) stockEditorAction('OpenStockEditor');
            else if (!$('stock-editor').open && editor && editor.open) stockEditorAction('CloseStockEditor');
        });
        $('stock-editor-search').addEventListener('input', filterStockEditor);
        $('btn-refresh-stock-editor').addEventListener('click', () => stockEditorAction('RefreshStockEditor'));
        $('btn-retry-market-upload').addEventListener('click', retryMarketUpload);
        $('btn-import-local').addEventListener('click', () => localBackupAction('ImportLocalProgress'));
        $('btn-export-local').addEventListener('click', () => localBackupAction('ExportLocalProgress'));
        $('btn-discard-stock-editor').addEventListener('click', () => {
            if (!$('btn-discard-stock-editor').disabled) stockEditorAction('DiscardStockEditorEdit');
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
