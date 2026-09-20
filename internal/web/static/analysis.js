// The coaching page starts its work with a POST and then waits. This polls
// the same URL for the answer, in the house style: one IIFE, no framework,
// and defensive about every element it reaches for, because the page is
// rendered by the server and is correct without any of this running.
(function () {
    'use strict';
    var box = document.getElementById('analysis');
    if (!box || box.dataset.state !== 'running') return;

    // Start gentle and back off. The work is seconds today and may be
    // minutes once a model is doing the thinking, so a fixed one-second
    // poll would be thousands of requests for one answer.
    var wait = 1000, MAX = 15000;
    var stopAt = Date.now() + 15 * 60 * 1000;

    function done(state) {
        return state === 'done' || state === 'failed';
    }

    function poll() {
        if (Date.now() > stopAt) return;
        fetch(window.location.href, {
            headers: { 'Accept': 'application/json' },
            credentials: 'same-origin'
        }).then(function (r) {
            return r.ok ? r.json() : null;
        }).then(function (status) {
            // A reload is what renders the answer. The server already knows
            // how to draw every state, so asking it again is less code than
            // a second rendering path here — and it keeps the page correct
            // for anyone without this script.
            if (status && done(status.state)) {
                window.location.reload();
                return;
            }
            wait = Math.min(wait * 1.5, MAX);
            setTimeout(poll, wait);
        }).catch(function () {
            // A blip is not an answer. Keep waiting, more slowly.
            wait = Math.min(wait * 2, MAX);
            setTimeout(poll, wait);
        });
    }
    setTimeout(poll, wait);
})();
