"use strict";

// Keep the form and the preview in sync with the backend frpc.toml generator.
// No browser/Node packages required: only the existing Vue options are loaded.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const page = fs.readFileSync(path.join(__dirname, "reverse_socks.html"), "utf8");
const scripts = [...page.matchAll(/<script>([\s\S]*?)<\/script>/g)];
assert.ok(scripts.length, "missing inline Vue script");

let options;
const globals = {
    siderDrawer: null,
    Vue: class {
        constructor(config) {
            options = config;
        }
    }
};
new vm.Script(scripts.at(-1)[1], { filename: "reverse_socks.html" })
    .runInNewContext(globals);
assert.ok(options, "Vue options were not registered");

for (const field of ["frpcUser", "xtcpVisitorName"]) {
    assert.ok(Object.hasOwn(options.data.form, field), `missing form.${field} default`);
    assert.ok(page.includes(`v-model.trim="form.${field}"`),
        `missing editable Web input for ${field}`);
    assert.ok(options.methods.applyView.toString().includes(`'${field}'`),
        `missing GET/save response hydration of ${field}`);
}
for (const field of ["targetSocksAddr", "targetSocksUsername", "targetSocksPassword"]) {
    assert.ok(Object.hasOwn(options.data.form, field), `missing Go Agent ${field} default`);
    assert.ok(page.includes(`form.${field}`), `missing Go Agent ${field} editor`);
    assert.ok(options.methods.applyView.toString().includes(`'${field}'`),
        `missing loaded Go Agent ${field} value`);
}

const form = {
    ...options.data.form,
    frpcUser: "0123456789abcdef",
    xtcpVisitorName: "overseas-xtcp-visitor",
    xtcpServerName: "mainland-proxy",
    warpAddr: "127.0.0.1:40000",
    targetSocksAddr: "127.0.0.1:40001",
    targetSocksUsername: "xray-user",
    targetSocksPassword: "xray-password"
};
const preview = options.computed.frpcTemplate.call({ form });
assert.ok(preview.includes('user = "0123456789abcdef"'));
assert.ok(preview.includes('name = "overseas-xtcp-visitor"'));
assert.ok(preview.includes('serverName = "mainland-proxy"'));
assert.ok(preview.indexOf('user = "0123456789abcdef"') <
    preview.indexOf("[[visitors]]"), "user must be at TOML root");
assert.ok(preview.indexOf('name = "overseas-xtcp-visitor"') >
    preview.indexOf("[[visitors]]"), "Visitor name must belong to [[visitors]]");
assert.ok(!preview.includes('name = "rsx3_outside"'),
    "preview must not replace configured Visitor name with a constant");
assert.ok(preview.includes('transport.proxyURL = "socks5://127.0.0.1:40000"'),
    "frpc must continue using WARP SOCKS5");
assert.ok(preview.includes('udpProxyURL = "socks5://127.0.0.1:40000"'),
    "frpc/STUN/XTCP must continue using WARP SOCKS5 UDP");
assert.ok(!preview.includes('40001') && !preview.includes("xray-user"),
    "Go Agent target SOCKS5 must never be used for frpc/STUN/XTCP");

const loaded = {
    form: { ...options.data.form },
    status: null,
    frpc: null
};
options.methods.applyView.call(loaded, {
    ...form,
    enabled: true,
    status: { state: "online" },
    frpc: { state: "running" }
});
assert.equal(loaded.form.frpcUser, form.frpcUser);
assert.equal(loaded.form.xtcpVisitorName, form.xtcpVisitorName);
assert.equal(loaded.form.xtcpServerName, form.xtcpServerName);
assert.equal(loaded.form.targetSocksAddr, form.targetSocksAddr);
assert.equal(loaded.form.targetSocksUsername, form.targetSocksUsername);
assert.equal(loaded.form.targetSocksPassword, form.targetSocksPassword);

console.log("x-ui reverse SOCKS5 frpc user and Visitor name Web wiring PASS");
