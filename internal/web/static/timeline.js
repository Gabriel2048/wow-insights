// wowinsight — the fight page's script. Loaded with defer, before Wowhead's tooltips.js,
// so the tooltip configuration below is in place when that script runs.
var whTooltips = {colorLinks: false, iconizeLinks: false, renameLinks: false};

(function () {
    var inner = document.getElementById('tlInner');
    if (!inner) return; // no player selected, so no timeline on the page

    var frame = document.getElementById('tlFrame');
    var crosshair = document.getElementById('crosshair');
    var readout = document.getElementById('dpsReadout');
    var dpsLane = document.getElementById('dpsLane');

    var scroll = inner.parentElement;
    var ruler = document.getElementById('ruler');
    var range = document.getElementById('zoomRange');
    var label = document.getElementById('zoomLabel');
    var buttons = document.querySelectorAll('.tl-controls button.zoom');
    var durationSec = (parseInt(inner.dataset.durationMs, 10) || 0) / 1000;
    var totalSec = (parseInt(inner.dataset.totalMs, 10) || 0) / 1000 || durationSec;
    var leadSec = (parseInt(inner.dataset.leadMs, 10) || 0) / 1000;
    var zoom = 1;

    // Candidate spacings for the time ruler, fine to coarse. The first one
    // that leaves enough room between labels wins, so zooming in reveals
    // finer gradations rather than sticking at the coarsest.
    var STEPS = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600];

    function stamp(sec) {
        var m = Math.floor(sec / 60), s = Math.round(sec % 60);
        return m + ':' + (s < 10 ? '0' : '') + s;
    }

    // Marks are labelled in fight time, but positioned on the drawn span,
    // which starts before the pull.
    function at(sec) { return 100 * (sec + leadSec) / totalSec; }

    function drawRuler() {
        if (!totalSec) return;
        var width = inner.clientWidth;
        var pxPerSec = width / totalSec;
        var step = STEPS[STEPS.length - 1];
        for (var i = 0; i < STEPS.length; i++) {
            if (STEPS[i] * pxPerSec >= 70) { step = STEPS[i]; break; }
        }
        var html = '';
        for (var t = 0; t <= durationSec; t += step) {
            html += '<div class="mark" style="left:' + at(t) +
                    '%"><span>' + stamp(t) + '</span></div>';
        }
        ruler.innerHTML = html;
    }

    // Rotated labels take horizontal room, so at any zoom several can land
    // on top of one another. Two casts can even share a timestamp: a
    // precast lands as the next cast bar begins. Labels are stacked into
    // rows so every one stays readable.
    var LABEL_ROWS = 2;
    var LABEL_COS = Math.cos(55 * Math.PI / 180); // matches .tname rotation
    var LABEL_PAD = 8;
    var labelled = Array.prototype.slice
        .call(inner.querySelectorAll('.track .tick, .boss .bcast, .track .cdblock, .raidlane .rcdblock'))
        .filter(function (t) {
            return t.classList.contains('cdblock') || t.classList.contains('rcdblock')
                || t.querySelector('.tname, .bname');
        });

    function layoutLabels() {
        // Runs at every zoom: cooldown chips are always on show, even when
        // the angled cast labels are not.
        var measured = [];
        labelled.forEach(function (tick) {
            var raid = tick.classList.contains('rcdblock');
            var block = raid || tick.classList.contains('cdblock');
            var span = block ? tick.querySelector('span') : tick.querySelector('.tname, .bname');
            if (!span) return;
            // A hidden label has no width and needs no room.
            if (!block && !span.offsetWidth) return;
            measured.push({
                span: block ? tick : span,
                lane: raid ? 'rcd' + (tick.dataset.row || '0') : (block ? 'cd' + (tick.dataset.row || '0')
                    : (tick.classList.contains('bcast') ? 'boss'
                        : (tick.classList.contains('woven') ? 'woven' : 'main'))),
                x: tick.offsetLeft,
                // A block claims whichever is wider, the bar or its name;
                // angled labels only claim their horizontal projection.
                w: block ? Math.max(tick.offsetWidth, span.offsetWidth + 4) + LABEL_PAD
                    : span.offsetWidth * LABEL_COS + LABEL_PAD
            });
        });

        // Raid cooldowns are already stacked into rows server-side, so each
        // row packs its labels independently.
        var free = {};
        function rowsFor(lane) {
            if (!free[lane]) {
                free[lane] = [];
                for (var k = 0; k < LABEL_ROWS; k++) free[lane].push(-Infinity);
            }
            return free[lane];
        }
        measured.forEach(function (m) {
            var rows = rowsFor(m.lane);
            // A block already occupies its own row; its label may not move.
            var limit = (m.lane.indexOf('cd') === 0 || m.lane.indexOf('rcd') === 0) ? 1 : LABEL_ROWS;
            var row = 0;
            while (row < limit && rows[row] > m.x) row++;
            if (row === limit) {
                // No room in any row. Hide it rather than print it over a
                // neighbour; the tooltip still names the cast, and zooming
                // in brings the label back.
                m.span.classList.add('crowded');
                return;
            }
            m.span.classList.remove('crowded');
            rows[row] = m.x + m.w;
            m.span.style.setProperty('--row', row);
        });
    }

    function setZoom(z, keepCentre) {
        z = Math.min(24, Math.max(1, z));
        // Hold the midpoint of the current view steady while zooming.
        var centre = keepCentre && inner.clientWidth
            ? (scroll.scrollLeft + scroll.clientWidth / 2) / inner.clientWidth
            : null;

        zoom = z;
        inner.style.width = (z * 100) + '%';
        inner.classList.toggle('labels', z >= 6);
        if (frame) frame.classList.toggle('labels', z >= 6);
        label.textContent = z.toFixed(1) + '\u00d7';
        range.value = z;
        buttons.forEach(function (b) {
            b.classList.toggle('active', parseFloat(b.dataset.zoom) === z);
        });
        drawRuler();
        layoutLabels();

        if (centre !== null) {
            scroll.scrollLeft = centre * inner.clientWidth - scroll.clientWidth / 2;
        }
    }

    buttons.forEach(function (b) {
        b.addEventListener('click', function () { setZoom(parseFloat(b.dataset.zoom), true); });
    });
    range.addEventListener('input', function () { setZoom(parseFloat(range.value), true); });

    // Ctrl/Cmd + wheel zooms, matching the gesture people expect on a chart.
    scroll.addEventListener('wheel', function (e) {
        if (!e.ctrlKey && !e.metaKey) return;
        e.preventDefault();
        setZoom(zoom * (e.deltaY < 0 ? 1.15 : 1 / 1.15), true);
    }, {passive: false});


    // --- idle threshold -------------------------------------------------
    // Every pause is already in the DOM; the threshold only decides which
    // ones are shown. Filtering client-side keeps it instant and avoids
    // re-querying Warcraft Logs on every adjustment.
    var DEFAULT_IDLE_MS = 1500;
    var STORAGE_KEY = 'wowinsight.idleMs';

    var idleInput = document.getElementById('idleMs');
    var idleSummary = document.getElementById('idleSummary');
    var statPauses = document.getElementById('statPauses');
    var statPausesLabel = document.getElementById('statPausesLabel');
    var statIdle = document.getElementById('statIdle');
    var idleRows = Array.prototype.slice.call(document.querySelectorAll('tr.idlerow'));
    var idleBands = Array.prototype.slice.call(document.querySelectorAll('.track .gap'));

    function humanMS(ms) {
        if (ms < 1000) return ms + 'ms';
        var s = ms / 1000;
        if (s < 60) return s.toFixed(1) + 's';
        return Math.floor(s / 60) + 'm ' + ('0' + Math.round(s % 60)).slice(-2) + 's';
    }

    function applyIdleThreshold(ms) {
        var shown = 0, total = 0;
        idleRows.forEach(function (row) {
            var v = parseInt(row.dataset.ms, 10);
            var visible = v >= ms;
            row.style.display = visible ? '' : 'none';
            if (visible) { shown++; total += v; }
        });
        idleBands.forEach(function (band) {
            band.style.display = parseInt(band.dataset.ms, 10) >= ms ? '' : 'none';
        });
        statPauses.textContent = shown;
        statPausesLabel.textContent = 'Pauses \u2265' + humanMS(ms);
        statIdle.textContent = humanMS(total);
        idleSummary.textContent = shown + ' of ' + idleRows.length + ' pauses shown';
    }

    function setIdleThreshold(ms, save) {
        if (!isFinite(ms) || ms < 0) ms = DEFAULT_IDLE_MS;
        applyIdleThreshold(ms);
        if (save) {
            try { localStorage.setItem(STORAGE_KEY, ms); } catch (e) { /* private mode */ }
        }
    }

    if (idleInput) {
        var saved = null;
        try { saved = localStorage.getItem(STORAGE_KEY); } catch (e) { /* private mode */ }
        if (saved !== null) idleInput.value = saved;
        idleInput.addEventListener('input', function () {
            setIdleThreshold(parseInt(idleInput.value, 10), true);
        });
        setIdleThreshold(parseInt(idleInput.value, 10), false);
    }

    // --- hover readout --------------------------------------------------
    // The curve is evenly spaced, so a time maps straight to a bucket
    // index; no search is needed.
    var takenLane = document.getElementById('takenLane');

    function seriesFrom(lane) {
        if (!lane || !lane.dataset.values) return null;
        return {
            values: lane.dataset.values.split(',').map(Number),
            start: parseInt(lane.dataset.startMs, 10) || 0,
            interval: parseInt(lane.dataset.intervalMs, 10) || 0
        };
    }

    var dpsSeries = seriesFrom(dpsLane);
    var takenSeries = seriesFrom(takenLane);

    // Buckets are evenly spaced, so a time maps straight to an index.
    function sampleAt(series, sec) {
        if (!series || !series.interval) return null;
        var i = Math.round((sec * 1000 - series.start) / series.interval);
        return (i >= 0 && i < series.values.length) ? series.values[i] : null;
    }
    var IDLE_READOUT = 'hover the timeline for a reading';

    function compactNum(v) {
        if (v >= 1e9) return (v / 1e9).toFixed(2) + 'b';
        if (v >= 1e6) return (v / 1e6).toFixed(2) + 'm';
        if (v >= 1e3) return (v / 1e3).toFixed(1) + 'k';
        return Math.round(v).toString();
    }

    function signedStamp(sec) {
        return (sec < 0 ? '-' : '') + stamp(Math.abs(sec));
    }

    var pendingFrame = 0;
    var lastReadout = '';
    var lastLeft = '';

    function paintHover(clientX) {
        var box = inner.getBoundingClientRect();
        var fraction = (clientX - box.left) / box.width;
        if (fraction < 0 || fraction > 1) return;

        var left = (fraction * 100).toFixed(3) + '%';
        if (left !== lastLeft) {
            crosshair.style.left = left;
            lastLeft = left;
        }
        if (crosshair.hidden) crosshair.hidden = false;

        var sec = fraction * totalSec - leadSec;
        var text = signedStamp(sec);
        var dps = sampleAt(dpsSeries, sec);
        var taken = sampleAt(takenSeries, sec);
        if (dps === null && taken === null) {
            text += '  \u2014  before the pull';
        } else {
            if (dps !== null) text += '  \u2014  ' + compactNum(dps) + ' DPS';
            if (taken !== null) text += '  \u00b7  ' + compactNum(taken) + ' taken';
        }
        // Writing the same text again would still dirty layout. The readout
        // exists only when the player has a damage graph — a healer, or
        // anyone who died at the pull, has none — so it is checked here
        // rather than assumed, or hovering their timeline would throw once
        // per animation frame.
        if (readout && text !== lastReadout) {
            readout.textContent = text;
            lastReadout = text;
        }
    }

    inner.addEventListener('mousemove', function (e) {
        var x = e.clientX;
        if (pendingFrame) return;
        pendingFrame = requestAnimationFrame(function () {
            pendingFrame = 0;
            paintHover(x);
        });
    });

    inner.addEventListener('mouseleave', function () {
        crosshair.hidden = true;
        if (readout && lastReadout !== IDLE_READOUT) {
            readout.textContent = IDLE_READOUT;
            lastReadout = IDLE_READOUT;
        }
    });

    window.addEventListener('resize', drawRuler);
    setZoom(1, false);
})();
