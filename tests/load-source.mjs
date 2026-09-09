import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const root = fileURLToPath(new URL("../", import.meta.url));
const requireDependency = createRequire(import.meta.url);

// 每个测试加载独立的真实源码模块，只有显式提供的宿主接口被替换。
// 使用已有 TypeScript 编译器，不依赖浏览器、Tauri 进程或额外测试框架。
export function loadSource(path, mockImport = () => undefined) {
  const modules = new Map();
  function load(filename) {
    if (modules.has(filename)) return modules.get(filename).exports;
    const module = { exports: {} };
    modules.set(filename, module);
    const { outputText } = ts.transpileModule(readFileSync(filename, "utf8"), {
      compilerOptions: {
        target: ts.ScriptTarget.ES2022,
        module: ts.ModuleKind.CommonJS,
        jsx: ts.JsxEmit.ReactJSX,
        esModuleInterop: true,
      },
      fileName: filename,
    });
    const require = (specifier) => {
      const mock = mockImport(specifier);
      if (mock !== undefined) return mock;
      if (specifier.startsWith("@/")) {
        return load(resolve(root, "src", `${specifier.slice(2)}.ts`));
      }
      if (specifier.startsWith(".")) {
        return load(resolve(dirname(filename), `${specifier}.ts`));
      }
      return requireDependency(specifier);
    };
    new Function("require", "module", "exports", outputText)(require, module, module.exports);
    return module.exports;
  }
  return load(resolve(root, path));
}
