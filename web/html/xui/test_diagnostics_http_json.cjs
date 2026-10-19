"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const path = require("node:path");

const root = path.resolve(__dirname, "../..");
const requests = [];
let beforeRequest;
let diagnosticsComponent;

const axios = {
    defaults: { headers: { post: {}, common: {} }, baseURL: "/" },
    interceptors: { request: { use(handler) { beforeRequest = handler; } } },
    async post(url, data, opts = {}) {
        const headers = Object.assign(
            {},
            this.defaults.headers.post,
            opts.headers || {}
        );
        const config = beforeRequest({ method: "post", url, headers, data });
        requests.push(config);
        let obj = {};
        if (url.endsWith("/diagnostics/snapshot")) {
            obj = { enabled: false, settings: { paused: false }, alerts: { active: [] } };
        }
        if (url.endsWith("/diagnostics/history")) obj = { points: [], summary: {} };
        if (url.endsWith("/diagnostics/reports")) obj = [];
        return { data: { success: true, msg: "", obj } };
    }
};
const Qs = {
    stringify(obj) {
        return Object.entries(obj || {})
            .map(([key, value]) => encodeURIComponent(key) + "=" + encodeURIComponent(value))
            .join("&");
    }
};
class Msg {
    constructor(success, msg, obj) {
        this.success = success;
        this.msg = msg;
        this.obj = obj;
    }
}
const Vue = {
    prototype: { $message: { error() {}, success() {} } },
    component(name, options) {
        assert.equal(name, "rsx-diagnostics");
        diagnosticsComponent = options;
    }
};
const context = {
    axios, Qs, Msg, Vue, console, Date, Math, JSON,
    window: { location: { pathname: "/private/xui/reverse-socks" }, confirm: () => true },
    setInterval: () => 1, clearInterval() {}
};
const run = (file, suffix = "") => vm.runInNewContext(
    fs.readFileSync(file, "utf8") + suffix,
    context, { filename: file }
);
run(path.join(root, "assets/js/axios-init.js"));
run(path.join(root, "assets/js/util/utils.js"), "\nthis.HttpUtil = HttpUtil;");
const page = fs.readFileSync(path.join(__dirname, "component/rsx_diagnostics.html"), "utf8");
const markup = page.match(/\{\{define "component\/rsxDiagnosticsMarkup"\}\}([\s\S]*?)\{\{end\}\}/)[1];
const script = page.match(/<script>([\s\S]*?)<\/script>/)[1]
    .replace('{{template "component/rsxDiagnosticsMarkup"}}', markup);
runSource(script);
function runSource(source) {
    vm.runInNewContext(source, context, { filename: "component/rsx_diagnostics.html" });
}
const model = diagnosticsComponent.data();
for (const [name, method] of Object.entries(diagnosticsComponent.methods)) {
    model[name] = method.bind(model);
}
for (const [name, computed] of Object.entries(diagnosticsComponent.computed)) {
    Object.defineProperty(model, name, { get: () => computed.call(model) });
}
model.$message = Vue.prototype.$message;

(async () => {
    await model.loadHistory();
    await model.saveSettings();
    model.snapshot = { enabled: false, settings: { paused: false }, alerts: { active: [] } };
    await model.togglePause();
    await model.startTask("self_test");
    await model.startTask("raw_benchmark");
    await model.cancelTask("running-id");
    await model.traceSession("epoch:sid");
    await model.deleteReport("example-report");

    const jsonEndpoints = ["history", "settings", "task", "cancel", "trace", "delete-report"];
    for (const endpoint of jsonEndpoints) {
        const matching = requests.filter(r => r.url.endsWith("/diagnostics/" + endpoint));
        assert.ok(matching.length, "UI did not exercise Diagnostics JSON endpoint " + endpoint);
        for (const request of matching) {
            assert.match(request.headers["Content-Type"], /^application\/json\b/i);
            assert.equal(request.data[0], "{", "Diagnostics request became a form body: " + request.data);
            const parsed = JSON.parse(request.data);
            assert.ok(parsed && typeof parsed === "object");
        }
    }
    assert.equal(
        JSON.parse(requests.find(r => r.url.endsWith("/diagnostics/history")).data).minutes,
        60
    );
    // Existing panel APIs must NOT switch to JSON globally.
    await context.HttpUtil.post("/xui/reverse-socks/config", { minutes: 60 });
    const legacy = requests.at(-1);
    assert.match(legacy.headers["Content-Type"], /^application\/x-www-form-urlencoded/i);
    assert.equal(legacy.data, "minutes=60");

    console.log("RSX Diagnostics JSON POST bodies, six handlers and existing form endpoints PASS");
})().catch(e => { console.error(e); process.exitCode = 1; });
