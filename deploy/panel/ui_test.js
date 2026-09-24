const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const sandbox = {
  URL,
  document: { querySelectorAll: () => [], addEventListener: () => {} },
  $: () => ({}),
  esc: value => String(value ?? "").replace(/[&<>]/g, char => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" })[char]),
};
vm.createContext(sandbox);
vm.runInContext(fs.readFileSync(path.join(__dirname, "static", "instructions.js"), "utf8"), sandbox);

const render = value => vm.runInContext(`instructionMarkdown(${JSON.stringify(value)})`, sandbox);
assert.match(render("**жирный**"), /<strong>жирный<\/strong>/);
assert.doesNotMatch(render('<img src=x onerror="alert(1)">'), /<img/);
assert.match(render('<img src=x onerror="alert(1)">'), /&lt;img/);
assert.doesNotMatch(render("[bad](javascript:alert(1))"), /href=/);
assert.match(render("[ok](https://example.com/?q=1&x=2)"), /href="https:\/\/example.com\/\?q=1&amp;x=2"/);
console.log("UI Markdown safety checks passed");
