const fs = require('fs');
const path = require('path');

function walk(dir) {
  let results = [];
  const list = fs.readdirSync(dir);
  for (const file of list) {
    const full = path.join(dir, file);
    const stat = fs.statSync(full);
    if (stat && stat.isDirectory()) {
      results = results.concat(walk(full));
    } else if (full.endsWith('.tsx')) {
      results.push(full);
    }
  }
  return results;
}

const files = walk('c:/Users/dandr/Documents/GitHub/D-PlaneOS/app-react/src');
let count = 0;

for (const file of files) {
  let content = fs.readFileSync(file, 'utf8');
  
  // Exclude React components that natively take title mapping: Modal, ErrorState, SectionCard, SensorSection, Alert
  // A simplistic multi-line regex for <tag ... title="val" ...> or <tag ... title={val} ...>
  // We'll just look for title= and print the surrounding text to debug.
  
  const matches = [...content.matchAll(/<([a-zA-Z0-9]+)[^>]*?\btitle\s*=\s*(['"{`])/g)];
  let foundInFile = false;
  for (const match of matches) {
    const tag = match[1];
    if (!['Modal', 'ErrorState', 'SectionCard', 'SensorSection', 'Alert'].includes(tag)) {
        console.log(`Found in ${file} on tag <${tag}>`);
        // console.log(match[0].slice(0, 100)); // sample
        count++;
    }
  }
}
console.log('Total:', count);
