// 本地合成 HTTP/认证代理与真实 Chromium，绝不访问外网。
import { spawn } from "node:child_process";
import http from "node:http";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";

const cli = fileURLToPath(new URL("./playwright-cli.mjs",import.meta.url));
const work = fs.mkdtempSync(path.join(os.tmpdir(),"artex-browser-proof-"));
const html = title => `<html><head><title>${title}</title></head><body><h1>${title}</h1></body></html>`;
const origin=http.createServer((req,res)=>{res.setHeader("Content-Type","text/html");res.end(html("DIRECT"));});
const counts={A:0,B:0};
const proxies=[];
for (const name of ["A","B"]) {
  const proxy=http.createServer((req,res)=>{
    if (req.headers["proxy-authorization"]!=="Basic "+Buffer.from("fixture-user:fixture-pass").toString("base64")) {
      res.writeHead(407,{"Proxy-Authenticate":'Basic realm="fixture"'});res.end();return;
    }
    counts[name]++;res.setHeader("Content-Type","text/html");res.end(html(name));
  });
  proxies.push(proxy);
}
await Promise.all([origin,...proxies].map(s=>new Promise(resolve=>s.listen(0,"127.0.0.1",resolve))));
const address=`http://127.0.0.1:${origin.address().port}/fixture`;
function run(args,env) {
  return new Promise((resolve,reject)=>{
    const child=spawn(process.execPath,[cli,...args],{cwd:work,env:{...process.env,...env},timeout:55000});
    let output="";child.stdout.on("data",v=>output+=v);child.stderr.on("data",v=>output+=v);
    child.on("error",reject);child.on("exit",code=>code===0?resolve(output):reject(new Error(`CLI ${args[0]} exit=${code}: ${output}`)));
  });
}
try {
  for (const [i,label] of ["A","B","DIRECT"].entries()) {
    const env={ARTEX_PLAYWRIGHT_MANAGED:"1",ARTEX_PLAYWRIGHT_RUN:`proof-${i}`,ARTEX_PLAYWRIGHT_PROXY:i<2?`http://fixture-user:fixture-pass@127.0.0.1:${proxies[i].address().port}`:"",ARTEX_PLAYWRIGHT_CA:""};
    try {
      await run(["open",address],env);
      assert.match(await run(["eval","document.title"],env),new RegExp(label));
      if (i===0) {
        await run(["screenshot",`--filename=${path.join(work,"browser.png")}`],env);
        await run(["pdf",`--filename=${path.join(work,"browser.pdf")}`],env);
        await run(["video-start",path.join(work,"browser.webm")],env);
        await run(["run-code","async page => { await page.waitForTimeout(1200); }"],env);
        await run(["video-stop"],env);
        const video=fs.readFileSync(path.join(work,"browser.webm"));
        assert.ok(video.length>500);assert.equal(video.subarray(0,4).toString("hex"),"1a45dfa3");
        assert.ok(fs.statSync(path.join(work,"browser.png")).size>500);
        assert.equal(fs.readFileSync(path.join(work,"browser.pdf")).subarray(0,4).toString(),"%PDF");
      }
    } finally { await run(["cleanup"],env); }
  }
  assert.ok(counts.A>0 && counts.B>0);
  console.log(JSON.stringify({result:"PASS",cases:["root 无头 Chromium","Basic代理 A→B→direct","截图","PDF","录像真实落盘","每轮清理"],counts,artifacts:work}));
} finally { for(const s of [origin,...proxies]) s.closeAllConnections(); await Promise.all([origin,...proxies].map(s=>new Promise(resolve=>s.close(resolve)))); }
