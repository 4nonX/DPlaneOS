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
    for t in re.findall(r'var \w+\s+(?:\[\])?\*?(\w+)\b', body) +re.findall(r'\w+ := &?(\w+)\{\}', body):
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
        if src[m.end():].startswith('{'):
            keys = object_keys(src, m.end())
        else:
            # a body built in a variable shortly before: const body = {...}
            # (or cond ? {...} : {...}); keys of every literal are checked
            vm = re.match(r'([A-Za-z_]\w*)\s*\)', src[m.end():])
            if not vm:
                continue
            decl = None
            for d in re.finditer(r'(?:const|let)\s+' + vm.group(1) + r'\b[^=\n]*=\s*', src[max(0, m.start() - 3000):m.start()]):
                decl = max(0, m.start() - 3000) + d.end()
            if decl is None:
                continue
            keys, j = [], decl
            while True:
                brace = src.find('{', j)
                stop = src.find('\n\n', decl)
                if brace < 0 or (0 <= stop < brace) or brace > m.start():
                    break
                keys += object_keys(src, brace)
                depth, k = 0, brace
                while k < len(src):  # skip to the end of this literal
                    if src[k] == '{':
                        depth += 1
                    elif src[k] == '}':
                        depth -= 1
                        if depth == 0:
                            break
                    k += 1
                nxt = src[k + 1:k + 40].lstrip()
                if not nxt.startswith(':'):
                    break
                j = k + 1
            if not keys:
                continue
        meth = m.group(1).upper()
        raw = m.group(3).split('?')[0]
        # ${...} segments match any route segment
        ui_rx = re.compile('^' + re.sub(r'\\\$\\\{[^}]*\\\}', '[^/]+', re.escape(raw)) + '$')
        line = src.count('\n', 0, m.start()) + 1
        name = os.path.basename(f)
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

# ── Responses: required keys a typed api.get<T>() expects that the handler
# never produces (a page that silently shows nothing). ──────────────────────
ALLOW_RESP = {
    'GET /api/jobs/{id}': 'encodes jobs.JobSnapshot (id, status, type, started_at)',
    'GET /api/monitoring/inotify': 'encodes monitoring.InotifyStats',
}


def produced(body, recv, depth=0):
    if body is None or depth > 3:
        return set()
    keys = set(re.findall(r'"([a-zA-Z_]\w*)":', body))
    keys |= set(re.findall(r'\w+\["(\w+)"\]\s*=', body))
    keys |= set(re.findall(r'json:"([^",]+)', body))
    for t in set(re.findall(r'\b(\w+)\{', body) + re.findall(r'\[\](\w+)', body) + re.findall(r'var \w+ \*?(\w+)', body)):
        keys |= named_struct(t)
    for helper in set(re.findall(r'\bh\.(\w+)\(', body)):
        keys |= produced(func_body(recv, helper), recv, depth + 1)
    for fn in set(re.findall(r'\b([A-Z]\w+)\(w, r\)', body)):  # delegation
        keys |= produced(func_body(None, fn), None, depth + 1)
    if 'CommandResponse' in body:
        keys |= {'success', 'output', 'error', 'code', 'guide', 'duration_ms', 'data'}
    return keys


def ts_match(s):
    d = 0
    for i, c in enumerate(s):
        if c == '{':
            d += 1
        elif c == '}':
            d -= 1
            if d == 0:
                return i + 1
    return len(s)


def ts_top(s):
    s = s[s.index('{') + 1: ts_match(s) - 1]
    out, d = [], 0
    for c in s:
        if c in '{<([':
            d += 1
        if d == 0:
            out.append(c)
        if c in '}>)]':
            d -= 1
    return ''.join(out)


def ts_split(s, sep):
    parts, d, cur = [], 0, ''
    for c in s:
        if c in '{<([':
            d += 1
        if c in '}>)]':
            d -= 1
        if c == sep and d == 0:
            parts.append(cur)
            cur = ''
        else:
            cur += c
    return parts + [cur]


def ts_keys(t, src, depth=0):
    """(required, all, resolvable) keys of a TypeScript type expression."""
    req, allk = set(), set()
    if depth > 3:
        return req, allk, False
    for part in [p.strip() for p in ts_split(t.strip(), '&')]:
        if part.startswith('{'):
            body = part
        elif re.match(r'^[A-Z]\w*$', part):
            m = re.search(r'interface ' + part + r'\b(?:\s+extends\s+([\w, &]+))?\s*\{', src)
            if not m:
                return req, allk, False
            body = src[m.end() - 1:]
            body = body[:ts_match(body)]
            if m.group(1):
                for base in re.split(r'[,&]', m.group(1)):
                    r2, a2, ok = ts_keys(base.strip(), src, depth + 1)
                    if not ok:
                        return req, allk, False
                    req |= r2
                    allk |= a2
        else:
            return req, allk, False
        for name, opt in re.findall(r'(?:^|[;,\n{]\s*)([A-Za-z_]\w*)(\??)\s*:', ts_top(body)):
            allk.add(name)
            if not opt:
                req.add(name)
    return req, allk, True


for f in sorted(glob.glob(os.path.join(SRC, '**', '*.ts*'), recursive=True)):
    if 'mock' in os.path.basename(f).lower():
        continue
    src = open(f, encoding='utf-8').read()
    for m in re.finditer(r'api\.get<', src):
        i, d = m.end(), 1
        while d and i < len(src):
            if src[i] == '<':
                d += 1
            elif src[i] == '>' and src[i - 1] != '=':
                d -= 1
            i += 1
        t = src[m.end():i - 1]
        pm = re.match(r"\(\s*(['`])(/api/[^'`]+)", src[i:])
        if not pm:
            continue
        raw = pm.group(2)
        if '${' in raw and '}' not in raw[raw.index('${'):]:
            raw = raw[:raw.index('${')]
        raw = re.sub(r'\$\{[^}]*\}', 'X', raw).split('?')[0]
        ui_rx = re.compile('^' + re.escape(raw).replace('X', '[^/]+') + '$')
        cands = [r for r in routes if r[1] == 'GET' and (r[0].match(raw) or ui_rx.match(r[3]))]
        if not cands or ('GET ' + cands[0][3]) in ALLOW_RESP:
            continue
        route = cands[0]
        body = handler_body(route[2]) if route[2] else None
        req, allk, ok = ts_keys(t, src)
        if body is None or not ok or not allk:
            continue
        have = produced(body, var_type.get(route[2].split('.')[0]))
        missing = sorted(k for k in req if k not in have and k != 'success')
        if missing:
            findings.append('%s:%d GET %s: %s never answers %s' % (
                os.path.basename(f), src.count('\n', 0, m.start()) + 1, route[3], route[2], missing))


for x in findings:
    print(x)
if findings:
    print('\n%d problem(s): request fields the API does not read, response fields it never sends, or calls without a route.'
          ' Fix the page or the handler, or add an ALLOW entry with the reason.' % len(findings), file=sys.stderr)
    sys.exit(1)
print('UI/API contract: ok (requests and responses)')
