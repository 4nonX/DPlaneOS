#!/usr/bin/env python3
"""Check that the web interface only sends fields its API handlers read.

For every api.post/put/patch/delete call in app-react with an object-literal
body, find the route in daemon/cmd/dplaned/main.go, the handler, the JSON
fields it decodes (request structs, map lookups, helpers it dispatches to),
and report UI keys the handler never reads: a renamed field on either side
silently breaks the page (the API decodes the request without error and
acts on empty values).

Heuristic by design; intentional cases go into ALLOW below.
Exit status 1 when there are findings.
"""
import glob
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, 'app-react', 'src')
D = os.path.join(ROOT, 'daemon')

# "file:METHOD /path key" -> reason
ALLOW = {
    'SnapshotSchedulerPage.tsx:POST /api/snapshots/run-now retain':
        'a manual snapshot is never pruned; retention applies to schedules',
}

main = open(os.path.join(D, 'cmd', 'dplaned', 'main.go'), encoding='utf-8').read()
gofiles = [f for f in glob.glob(os.path.join(D, 'internal', 'handlers', '*.go')) if not f.endswith('_test.go')]
allgo = '\n'.join(open(f, encoding='utf-8').read() for f in gofiles)

# handler variable -> type (fooHandler := handlers.NewFooHandler(...))
var_type = {}
for v, ctor in re.findall(r'(\w+)\s*:?=\s*handlers\.(New\w+)\(', main):
    m = re.search(r'\nfunc ' + ctor + r'\([^)]*\)\s*\*?(\w+)', allgo)
    if m:
        var_type[v] = m.group(1)

routes = []
for m in re.finditer(r'r\.Handle(?:Func)?\(\s*"([^"]+)"\s*,(.*?)\)\.Methods\(([^)]*)\)', main):
    path, expr, methods = m.group(1), m.group(2), m.group(3)
    cands = [c for c in re.findall(r'(\w+\.\w+)', expr)
             if not c.startswith(('middleware.', 'http.'))]
    handler = cands[-1] if cands else None
    rx = '^' + re.sub(r'\\\{[^}]+\\\}', '[^/]+', re.escape(path)) + '$'
    for meth in re.findall(r'"(\w+)"', methods):
        routes.append((re.compile(rx), meth, handler, path))


def block(text, start):
    """Text of the {...} block whose '{' is at or after start."""
    i = text.index('{', start)
    depth, j = 0, i
    while j < len(text):
        if text[j] == '{':
            depth += 1
        elif text[j] == '}':
            depth -= 1
            if depth == 0:
                return text[i:j + 1]
        j += 1
    return text[i:]


def func_body(recv_type, name):
    if recv_type:
        m = re.search(r'\nfunc \(\w+ \*?' + re.escape(recv_type) + r'\) ' + re.escape(name) + r'\(', allgo)
    else:
        m = re.search(r'\nfunc ' + re.escape(name) + r'\(', allgo)
    if not m:
        return None
    return block(allgo, allgo.index(')', m.end()) if recv_type else m.end())


def handler_body(handler):
    var, name = handler.split('.', 1)
    if var == 'handlers':
        body = func_body(None, name)
        # factory: func X(db) http.HandlerFunc { return handleX(db) }
        if body:
            for inner in re.findall(r'return (\w+)\(', body):
                ib = func_body(None, inner)
                if ib:
                    body += ib
        return body
    return func_body(var_type.get(var), name)


def named_struct(tname, seen=None):
    seen = seen or set()
    if tname in seen:
        return set()
    seen.add(tname)
    m = re.search(r'\ntype ' + re.escape(tname) + r' struct \{', allgo)
    if not m:
        return set()
    body = block(allgo, m.end() - 1)
    tags = set(re.findall(r'json:"([^",]+)', body))
    for emb in re.findall(r'^\s+(\w+)\s*$', body, re.M):
        tags |= named_struct(emb, seen)
    return tags


def fields(body, recv_type, depth=0):
    if body is None or depth > 3:
        return set()
    tags = set(re.findall(r'json:"([^",]+)', body))  # inline structs
    for t in re.findall(r'var \w+\s+\*?(\w+)\b', body) + re.findall(r'\w+ := &?(\w+)\{\}', body):
        tags |= named_struct(t)
    tags |= set(re.findall(r'\w+\["(\w+)"\]', body))
    # helpers the handler dispatches to (directly or through middleware)
    for helper in set(re.findall(r'\bh\.(\w+)\((?:w, r|w, req|w, r,)', body) + re.findall(r'http\.HandlerFunc\(h\.(\w+)\)', body)):
        tags |= fields(func_body(recv_type, helper), recv_type, depth + 1)
    return tags


call_re = re.compile(r"api\.(post|put|patch|delete)(?:<[^>]*>)?\(\s*(['`])([^'`]+)\2\s*,\s*")


def object_keys(src, i):
    depth, j, keys, at_key = 0, i, [], True
    while j < len(src):
        c = src[j]
        if c in '\'"`' and depth >= 1:
            q = c
            if depth == 1 and at_key:
                m = re.match(r"(['\"])([\w-]+)\1\s*:", src[j:])
                if m:
                    keys.append(m.group(2))
                    at_key = False
            j += 1
            while j < len(src) and src[j] != q:
                j += 2 if src[j] == '\\' else 1
        elif c in '{[(':
            depth += 1
            if depth == 1:
                at_key = True
        elif c in '}])':
            depth -= 1
            if depth == 0:
                break
        elif depth == 1:
            if c == ',':
                at_key = True
            elif at_key and not c.isspace():
                if src.startswith('...', j):
                    at_key = False
                else:
                    m = re.match(r'([A-Za-z_]\w*)', src[j:])
                    if m:
                        keys.append(m.group(1))
                        j += len(m.group(1)) - 1
                    at_key = False
        j += 1
    return keys


findings = []
for f in sorted(glob.glob(os.path.join(SRC, '**', '*.ts*'), recursive=True)):
    src = open(f, encoding='utf-8').read()
    for m in call_re.finditer(src):
        if not src[m.end():].startswith('{'):
            continue
        meth = m.group(1).upper()
        raw = m.group(3).split('?')[0]
        # ${...} segments match any route segment
        ui_rx = re.compile('^' + re.sub(r'\\\$\\\{[^}]*\\\}', '[^/]+', re.escape(raw)) + '$')
        line = src.count('\n', 0, m.start()) + 1
        name = os.path.basename(f)
        keys = object_keys(src, m.end())
        cands = [r for r in routes if r[1] == meth and (r[0].match(raw) or ui_rx.match(r[3]) or r[0].match(re.sub(r'\$\{[^}]*\}', 'X', raw)))]
        if not cands:
            findings.append('%s:%d %s %s: no such route' % (name, line, meth, raw))
            continue
        route = cands[0]
        body = handler_body(route[2]) if route[2] else None
        if body is None:
            continue  # handler not resolvable statically
        recv = var_type.get(route[2].split('.')[0])
        read = fields(body, recv)
        for k in keys:
            if k in read or ('%s:%s %s %s' % (name, meth, route[3], k)) in ALLOW:
                continue
            findings.append('%s:%d %s %s: %s ignores "%s"' % (name, line, meth, route[3], route[2], k))

# Every call (also GET and calls without a body) must have a route.
all_calls = re.compile(r"api\.(get|post|put|patch|delete)(?:<[^()]*?>)?\(\s*(['`])(/api/[^'`]+)")
for f in sorted(glob.glob(os.path.join(SRC, '**', '*.ts*'), recursive=True)):
    if 'mock' in os.path.basename(f).lower():
        continue
    src = open(f, encoding='utf-8').read()
    for m in all_calls.finditer(src):
        meth, raw = m.group(1).upper(), m.group(3)
        if '${' in raw and '}' not in raw[raw.index('${'):]:
            raw = raw[:raw.index('${')]  # query built in a nested template
        raw = re.sub(r'\$\{[^}]*\}', 'X', raw).split('?')[0]
        ui_rx = re.compile('^' + re.escape(raw).replace('X', '[^/]+') + '$')
        if not any(r[1] == meth and (r[0].match(raw) or ui_rx.match(r[3])) for r in routes):
            findings.append('%s:%d %s %s: no such route' % (os.path.basename(f), src.count('\n', 0, m.start()) + 1, meth, raw))

for x in findings:
    print(x)
if findings:
    print('\n%d problem(s): request fields the API does not read, or calls without a route.'
          ' Fix the page or the handler, or add an ALLOW entry with the reason.' % len(findings), file=sys.stderr)
    sys.exit(1)
print('UI/API contract: ok')
