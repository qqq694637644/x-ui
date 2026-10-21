const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, 'component/rsx_diagnostics.html'), 'utf8');
const markup = source.match(/\{\{define "component\/rsxDiagnosticsMarkup"\}\}([\s\S]*?)\{\{end\}\}/)[1];
const script = source.match(/<script>([\s\S]*?)<\/script>/)[1]
  .replace('{{template "component/rsxDiagnosticsMarkup"}}', markup);
let options;
const context = {Vue:{component(name, config){assert.equal(name,'rsx-diagnostics');options=config;}},
  window:{location:{pathname:'/secret/xui/reverse-socks'},confirm:()=>false},
  HttpUtil:{post:async()=>({success:false})},console, setInterval:()=>1,clearInterval:()=>{}};
vm.runInNewContext(script, context);
const model = options.data();
for(const [name,fn] of Object.entries(options.methods)) model[name] = fn.bind(model);
for(const [name,fn] of Object.entries(options.computed)) Object.defineProperty(model,name,{get:()=>fn.call(model)});
assert.equal(model.number(null,1),'--');
assert.equal(model.number(undefined,1),'--');
assert.equal(model.number(0,1),'0.00');
assert.equal(model.chartPaths.length,0);
const now = Date.now();
model.history={points:[
  {time:new Date(now).toISOString(),values:{node_download_bytes_per_second:1000000}},
  {time:new Date(now+5000).toISOString(),values:{node_download_bytes_per_second:null}},
  {time:new Date(now+10000).toISOString(),values:{node_download_bytes_per_second:2000000}},
]};
assert.equal(model.chartPaths.length,2,'missing data must break the line, not plot zero');
assert.equal(model.chartMax,2);
assert.equal(model.chartUnit,'MB/s');
assert.equal(model.downloadURL('abc','zip'),'/secret/xui/reverse-socks/diagnostics/download/abc/zip');
assert.equal(model.busy,false);
model.snapshot={tasks:[{state:'running'}],agent:{sessions:[]}};
assert.equal(model.busy,true);
for(const expected of ['diagnostics/history','diagnostics/task','diagnostics/trace','diagnostics/settings',
  'diagnostics/reports','capture','self_test','socks_benchmark','raw_benchmark','target_benchmark','egress_check']){
  assert.ok(script.includes(expected),'missing diagnostic function '+expected);
}
assert.ok(!script.includes('v-html'),'raw reports must not be inserted as unescaped HTML');
console.log('RSX Diagnostics UI: all four tabs, null/gap semantics, task gating and raw report routes PASS');
