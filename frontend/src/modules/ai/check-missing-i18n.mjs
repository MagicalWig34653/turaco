// Run from frontend. Dynamic key families are checked by tsc and catalog tests.
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import ts from 'typescript';
const root = 'src';
function parse(file) {
  return ts.createSourceFile(file, readFileSync(file, 'utf8'), ts.ScriptTarget.Latest, true);
}
const catalogs = ['en', 'de'].map((locale) => {
  const keys = new Set();
  function visit(node) {
    if (
      ts.isPropertyAssignment(node) &&
      (ts.isStringLiteral(node.name) || ts.isIdentifier(node.name))
    )
      keys.add(node.name.text);
    ts.forEachChild(node, visit);
  }
  visit(parse(`${root}/platform/i18n/messages.${locale}.ts`));
  return keys;
});
const missing = [];
function scan(path) {
  for (const entry of readdirSync(path, { withFileTypes: true })) {
    const file = join(path, entry.name);
    if (entry.isDirectory()) scan(file);
    else if (/\.tsx?$/.test(file)) {
      function visit(node) {
        if (
          ts.isCallExpression(node) &&
          ts.isIdentifier(node.expression) &&
          node.expression.text === 't'
        ) {
          const arg = node.arguments[0];
          if (arg && ts.isStringLiteral(arg) && catalogs.some((catalog) => !catalog.has(arg.text)))
            missing.push(`${file}: ${arg.text}`);
        }
        ts.forEachChild(node, visit);
      }
      visit(parse(file));
    }
  }
}
scan(root);
if (missing.length) {
  console.error(missing.join('\n'));
  process.exitCode = 1;
} else console.log('No missing literal i18n keys in frontend/src (en/de).');
