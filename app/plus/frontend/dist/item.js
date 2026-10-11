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
    let state = {
        enabled: true, automatic: true, language: 'en', mode: 'PVP', hasAPIKey: false,
        busy: false, status: 'no_key', error: '', ocrLanguages: [], cachedAt: '', stale: false, item: null, scanDetails: null,
        characterUid: '', characters: [], charactersLoading: false, charactersError: '', tracking: null,
    };

    const statusNames = {
        idle: 'Waiting for an item Inspect window.', ready: 'Waiting for an item Inspect window.', disabled: 'Automatic scanning is off.',
        scanning: 'Inspect title scanning…', searching: 'Searching for item…', no_key: 'API key required',
        no_match: 'Item not found', waiting: 'Waiting for the game',
    };

    const isBusy = () => localBusy || !!state.busy;
    const safeText = (value) => typeof value === 'string' ? value.slice(0, 500) : '';
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
    const canAddTracking = (tracking) => {
        const current = currentTracking();
        return (!!nativeApp() || preview) && !isBusy() && state.hasAPIKey && !!state.characterUid &&
            !!tracking && sameTrackingTarget(tracking) && tracking.tracked === true && tracking.requiredQuantity !== 0 &&
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
        const haveBothKinds = requirements.some((requirement) => requirement.kind === 'fir') &&
            requirements.some((requirement) => requirement.kind === 'regular');
        const mixed = haveBothKinds || tracked && tracking.requirement === 'mixed';
        const noRequiredQuantity = tracked && tracking.requiredQuantity === 0;
        const visibleRequirements = noRequiredQuantity || mixed && !haveBothKinds ? [] : requirements;
        const separateInventory = tracked && tracking.firInventorySeparate === true;
        const completedSources = tracked && Array.isArray(tracking.completedSources) ? tracking.completedSources.filter((source) =>
            source && typeof source === 'object' && !Array.isArray(source)) : [];
        panel.classList.toggle('hidden', noRequiredQuantity || !tracked && !error && !(tracking && tracking.loading) && !adding);
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
            label.textContent = requirement.kind === 'fir' ? 'Found In Raid' : 'Regular';
            const quantity = document.createElement('dd');
            quantity.textContent = itemsText(requirement.requiredQuantity) + ' required';
            row.append(label, quantity);
            return row;
        }));
        element('requirements').classList.toggle('hidden', !visibleRequirements.length);
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
        $('btn-search').disabled = !available || busy || !state.hasAPIKey || !$('search-name').value.trim();
        $('btn-scan').disabled = !available || busy || !state.hasAPIKey || !englishModelAvailable();
        $('btn-save-key').disabled = !available || busy || keyFromStorage || !$('api-key').value.trim();
        $('btn-clear-key').disabled = !available || busy || !state.hasAPIKey;
        $('btn-toggle-key').disabled = !available || localBusy || !state.hasAPIKey && !$('api-key').value;
        $('api-key').disabled = !available || localBusy;
        updateKeyVisibility();
        $('btn-item-page').disabled = !available || busy || !state.item || !state.item.uid;
        $('scan-automatic').disabled = !available;
        $('game-mode').disabled = !available;
        $('tracked-character').disabled = !available || localBusy || !state.hasAPIKey || !!state.charactersLoading ||
            !availableCharacters().length;
        const entries = trackingEntries();
        [...$('tracked-panels').children].forEach((panel, index) => {
            panel.querySelector('[data-tracked="add"]').disabled = !canAddTracking(entries[index]);
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
        state = { ...state, ...nextState };
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
        updateOption('game-mode', 'value', String(state.mode || 'PVP').toUpperCase());
        renderCharacters();
        updateOption('tracked-character', 'value', safeText(state.characterUid));
        renderTracking();

        $('key-indicator').textContent = state.hasAPIKey ? 'API key saved' : 'API key required';
        $('key-indicator').classList.toggle('saved', !!state.hasAPIKey);
        $('api-key').placeholder = state.hasAPIKey ? '••••••••••••••••' : 'Enter API key';
        $('mode-badge').textContent = String(state.mode || 'PVP').toUpperCase();
        const status = safeText(state.status);
        const automaticEnabled = !!state.enabled && !!state.automatic;
        const waitingStatus = ['', 'idle', 'ready', 'waiting', 'disabled', 'Waiting for an item Inspect window.',
            'Waiting for the game', 'Automatic scanning is off.'].includes(status);
        $('status-text').textContent = state.hasAPIKey && !automaticEnabled && waitingStatus ? statusNames.disabled :
            statusNames[status] || status || (state.hasAPIKey ? 'Waiting for an item Inspect window.' : 'API key required');
        $('status-dot').className = 'status-dot' + (state.error ? ' error' : isBusy() ? ' busy' : state.hasAPIKey && automaticEnabled ? ' ready' : '');
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
            $('settings').open = !state.hasAPIKey;
            initialRender = false;
        }
        updateControls();
    };

    const previewVariants = preview && new URLSearchParams(window.location.search).get('variants') === '1';
    const sampleItem = previewVariants ? {
        uid: 'preview-fuel-tank', name: 'Metal fuel tank', shortName: 'Fuel',
        price: 118000, avg24hPrice: 121600, traderName: 'Therapist', traderPrice: 28920, traderPriceCur: '₽', slots: 6,
        haveMarketData: true, bannedOnFlea: false,
    } : {
        uid: 'preview-item', name: 'Galvion Caiman Hybrid helmet (MultiCam Alpine)', shortName: 'Caiman',
        price: 162000, avg24hPrice: 154800, traderName: 'Ragman', traderPrice: 25660, traderPriceCur: '₽', slots: 4,
        haveMarketData: true, bannedOnFlea: false,
    };
    let previewState = {
        ...state, hasAPIKey: true, status: 'Item recognized.', ocrLanguages: ['en'], cachedAt: '2026-10-10T22:30:23Z', item: sampleItem,
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
    let previewAPIKey = 'preview-api-key';
    const call = async (method, ...args) => {
        const app = nativeApp();
        if (app && typeof app[method] === 'function') return app[method](...args);
        if (!preview) throw new Error('The app connection is unavailable.');
        if (method === 'RevealAPIKey') return previewState.hasAPIKey ? previewAPIKey : '';
        if (method === 'SetOptions') previewState = { ...previewState, ...args[0] };
        if (method === 'SaveAPIKey') {
            previewAPIKey = args[0];
            previewState = { ...previewState, hasAPIKey: true, status: 'ready' };
        }
        if (method === 'ClearAPIKey') {
            previewAPIKey = '';
            previewState = { ...previewState, hasAPIKey: false, status: 'no_key', item: null, characterUid: '', characters: [], tracking: null };
        }
        if (method === 'SearchItem' || method === 'ScanNow') previewState = { ...previewState, item: sampleItem, status: 'Item recognized.' };
        if (method === 'AddTrackedItem' && previewState.tracking && !previewState.tracking.error &&
            !previewState.tracking.variants && args[0] === previewState.tracking.itemUid) {
            const availableQuantity = previewState.tracking.availableQuantity + 1;
            previewState = { ...previewState, tracking: { ...previewState.tracking, availableQuantity,
                remainingQuantity: Math.max(0, previewState.tracking.requiredQuantity - availableQuantity), status: 'Added 1 item to Tracked Items stock.' } };
        }
        return { ...previewState };
    };
    const refresh = async (result) => {
        render(result && typeof result === 'object' ? result : await call('GetState'));
    };
    const action = async (method, args = [], success = '') => {
        if (isBusy()) return false;
        localBusy = true;
        showFeedback('');
        updateControls();
        try {
            await refresh(await call(method, ...args));
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
        language: 'en', mode: $('game-mode').value, characterUid: $('tracked-character').value,
    });
    const saveOptions = (event) => {
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
                showFeedback('Settings could not be saved. Please try again.', true);
                updateControls();
            }
        });
    };

    const addTrackedItem = async () => {
        const tracking = currentTracking();
        if (!canAddTracking(tracking)) return;
        const target = { itemUid: state.item.uid, charUid: state.characterUid };
        addingTracking = target;
        trackingFailure = null;
        localBusy = true;
        renderTracking();
        updateControls();
        try {
            const result = await call('AddTrackedItem', target.itemUid);
            if (sameTrackingTarget(target)) await refresh(result);
        } catch (_) {
            if (sameTrackingTarget(target)) trackingFailure = { ...target, message: 'The item could not be added. Please try again.' };
        } finally {
            addingTracking = null;
            localBusy = false;
            renderTracking();
            updateControls();
        }
    };

    const toggleAPIKey = async () => {
        if ((!nativeApp() && !preview) || localBusy || !$('settings').open) return;
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
