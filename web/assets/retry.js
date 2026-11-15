// One retry controller per operation. Callers decide what success means.
(function () {
    'use strict';
    window.openVibelyRetry = function (run, options) {
        options = options || {};
        var initial = options.initialDelay || 1000, maximum = options.maxDelay || 15000;
        var delay = initial, timer = null, active = false, disposed = false, generation = 0;
        function cancel() {
            clearTimeout(timer); timer = null; active = false; generation++;
        }
        function success() { cancel(); delay = initial; }
        function fail() {
            if (disposed || timer !== null) return;
            active = false; generation++;
            var wait = delay + (options.jitter ? Math.random() * options.jitter : 0);
            delay = Math.min(maximum, delay * 2);
            timer = setTimeout(function () { timer = null; start(); }, wait);
        }
        function start() {
            if (disposed || active || timer !== null) return false;
            active = true;
            var token = ++generation;
            var attempt = {
                current: function () { return !disposed && active && token === generation; },
                success: function () { if (attempt.current()) success(); },
                fail: function () { if (attempt.current()) fail(); }
            };
            try {
                var result = run(attempt);
                if (result && typeof result.catch === 'function') result.catch(attempt.fail);
            } catch (_) { attempt.fail(); }
            return true;
        }
        return {start:start, fail:fail, success:success, cancel:cancel,
            dispose:function () { cancel(); disposed = true; },
            get active() { return active; }};
    };
})();
