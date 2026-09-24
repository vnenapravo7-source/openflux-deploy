const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const elements = new Map();
const element = selector => {
  if (!elements.has(selector)) elements.set(selector, {classList: {add() {}, remove() {}}, value: "", scrollIntoView() {}, dispatchEvent() {}});
  return elements.get(selector);
};
let uploaded;
const sandbox = {
  URL,
  document: { querySelectorAll: () => [], addEventListener: () => {} },
  $: element,
  FormData: class { constructor(){this.parts=[]} append(name,value,fileName){this.parts.push([name,value,fileName])} },
  api: async (_path, options) => {uploaded=options.body},
  loadInstructions: async () => {},
  toast: () => {},
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
const html = fs.readFileSync(path.join(__dirname, "static", "index.html"), "utf8");
for (const [id, name] of [["instructionKind", "kind"], ["instructionMediaTitle", "title"], ["instructionFile", "file"]]) {
  assert.match(html, new RegExp(`id="${id}" name="${name}"`), `${id} must be included in FormData`);
}
(async () => {
  element("#instructionKind").value = "image";
  element("#instructionMediaTitle").value = "Sample";
  const file = {name: "sample.png"};
  element("#instructionFile").files = [file];
  await element("#instructionUploadForm").onsubmit({preventDefault() {}, target: element("#instructionUploadForm")});
  assert.deepEqual(uploaded.parts, [["kind", "image", undefined], ["title", "Sample", undefined], ["file", file, "sample.png"]]);
  console.log("UI Markdown and media upload checks passed");
})().catch(error => {console.error(error); process.exitCode = 1});

