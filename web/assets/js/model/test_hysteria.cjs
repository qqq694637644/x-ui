'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {webcrypto} = require('node:crypto');

const context = {
    crypto: webcrypto,
    URL,
    console,
    ObjectUtil: {
        isEmpty(value) {
            return value == null || value === '' ||
                (Array.isArray(value) && value.length === 0) ||
                (typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === 0);
        },
        isArrEmpty(value) { return !Array.isArray(value) || value.length === 0; },
        clone(value) { return value == null ? value : JSON.parse(JSON.stringify(value)); },
    },
    RandomUtil: {
        randomIntRange() { return 18085; },
        randomUUID() { return '01234567-89ab-cdef-0123-456789abcdef'; },
        randomSeq() { return 'test-placeholder'; },
    },
};
vm.createContext(context);
const source = fs.readFileSync(path.join(__dirname, 'xray.js'), 'utf8');
vm.runInContext(source + '\nthis.__classes={Protocols,Inbound,StreamSettings,HysteriaStreamSettings};',
    context, {filename:'web/assets/js/model/xray.js'});

const {Protocols, Inbound, StreamSettings} = context.__classes;
const inbound = new Inbound();
inbound.protocol = Protocols.HYSTERIA;
assert.equal(inbound.stream.network, 'hysteria');
assert.equal(inbound.stream.security, 'tls');
assert.deepEqual(Array.from(inbound.stream.tls.alpn), ['h3']);
assert.match(inbound.settings.clients[0].auth, /^[a-f0-9]{64}$/);
inbound.port = 18443;
inbound.listen = '::';
inbound.stream.tls.certs[0].certFile = '/secure/fullchain.pem';
inbound.stream.tls.certs[0].keyFile = '/secure/privkey.pem';
inbound.stream.hysteria.brutalUp = 16;
inbound.stream.hysteria.brutalDown = 24;

const wire = inbound.toJson();
assert.equal(wire.protocol, 'hysteria');
assert.equal(wire.settings.version, 2);
assert.equal(wire.settings.clients[0].auth, inbound.settings.clients[0].auth);
assert.deepEqual(Object.keys(wire.streamSettings.hysteriaSettings), ['version']);
assert.equal(wire.streamSettings.hysteriaSettings.version, 2);
assert.equal(wire.streamSettings.finalmask.quicParams.congestion, 'force-brutal');
assert.equal(wire.streamSettings.finalmask.quicParams.brutalUp, '16mbps');
assert.equal(wire.streamSettings.finalmask.quicParams.brutalDown, '24mbps');

const restored = Inbound.fromJson(JSON.parse(JSON.stringify(wire)));
assert.equal(restored.settings.clients[0].auth, inbound.settings.clients[0].auth);
assert.equal(restored.stream.hysteria.brutalUp, 16);
assert.equal(restored.stream.hysteria.brutalDown, 24);
assert.equal(restored.stream.network, 'hysteria');
assert.deepEqual(Array.from(restored.stream.tls.alpn), ['h3']);
assert.equal(restored.stream.toJson().finalmask.quicParams.congestion, 'force-brutal');

// Preserving expert Xray QUIC tuning does not change forced Brutal settings.
restored.stream.finalMask = JSON.stringify({
    quicParams: {keepAlivePeriod: 15},
    udp: [{type:'none',settings:{}}],
});
const preserved = restored.stream.toJson().finalmask;
assert.equal(preserved.quicParams.keepAlivePeriod, 15);
assert.equal(preserved.quicParams.congestion, 'force-brutal');
assert.equal(preserved.udp[0].type, 'none');

// Malformed saved rates must NOT silently revert to the default 16 Mbps.
const malformed = StreamSettings.fromJson({
    network: 'hysteria', security: 'tls',
    tlsSettings:{alpn:['h3'],certificates:[{certificateFile:'cert',keyFile:'key'}]},
    finalmask:{quicParams:{congestion:'reno',brutalUp:'broken',brutalDown:'24mbps'}},
});
assert.notEqual(malformed.hysteria.brutalUp, 16);
assert.equal(malformed.toJson().finalmask.quicParams.congestion, 'force-brutal');
assert.notEqual(malformed.toJson().finalmask.quicParams.brutalUp, '16mbps');

// Legacy inbound classes/settings must remain independent of Hysteria2.
const legacy = new Inbound();
assert.equal(legacy.protocol, Protocols.VMESS);
assert.equal(legacy.stream.network, 'tcp');
assert.equal(legacy.stream.toJson().hysteriaSettings, undefined);
restored.protocol = Protocols.VLESS;
assert.equal(restored.stream.network, 'tcp');
assert.equal(restored.stream.security, 'none');

// Optional CI/Windows integration fixture: Xray must parse the actual
// browser-model generated JSON, not just a hand-maintained example.
if (process.env.XUI_HYSTERIA_FIXTURE_PATH) {
    const generated = Inbound.fromJson(JSON.parse(JSON.stringify(wire)));
    generated.listen = '127.0.0.1';
    generated.port = Number(process.env.XUI_HYSTERIA_FIXTURE_PORT || 18443);
    generated.settings.clients[0].auth = process.env.XUI_HYSTERIA_FIXTURE_AUTH || '0123456789abcdef0123456789abcdef';
    generated.stream.tls.certs[0].certFile = process.env.XUI_HYSTERIA_CERT_PATH;
    generated.stream.tls.certs[0].keyFile = process.env.XUI_HYSTERIA_KEY_PATH;
    assert.ok(generated.stream.tls.certs[0].certFile && generated.stream.tls.certs[0].keyFile);
    const result = {
        log: {loglevel: 'debug'},
        inbounds: [generated.toJson()],
        outbounds: [{tag:'direct',protocol:'freedom',settings:{}}],
    };
    fs.writeFileSync(process.env.XUI_HYSTERIA_FIXTURE_PATH, JSON.stringify(result));
}

console.log('Native Xray 26.3.27 Hysteria2 model, TLS, forced Brutal and legacy isolation: PASS');
