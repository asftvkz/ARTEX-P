// Node 测试加载真实 TypeScript API 模块；不改生产构建或模块解析配置。

import ts from "typescript";

import { existsSync, readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const src = fileURLToPath(new URL("../src/", import.meta.url));
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier.startsWith("@/") || (specifier.startsWith(".") && context.parentURL?.startsWith("file:"))) {
      const base = specifier.startsWith("@/")
        ? resolve(src, specifier.slice(2))
        : resolve(dirname(fileURLToPath(context.parentURL)), specifier);
      for (const file of [base, `${base}.ts`, `${base}.tsx`, `${base}.mjs`, resolve(base, "index.ts")]) {
        if (existsSync(file) && /\.(ts|tsx|mjs)$/.test(file))
          return { url: pathToFileURL(file).href, shortCircuit: true };
      }
    }
    return nextResolve(specifier, context);
  },
  load(url, context, nextLoad) {
    if (/\.(ts|tsx)$/.test(url)) {
      return {
        format: "module",
        shortCircuit: true,
        source: ts.transpileModule(readFileSync(fileURLToPath(url), "utf8"), {
          fileName: fileURLToPath(url),
          compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.ReactJSX },
        }).outputText,
      };
    }
    return nextLoad(url, context);
  },
});
