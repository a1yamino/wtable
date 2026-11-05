#!/usr/bin/env python3
"""Exercise the built native desktop and real daemon in a disposable home.

Example: python3 scripts/native_smoke.py --binary .dev/native-e2e/darwin-arm64/wtable.app/Contents/MacOS/wtable --output .dev/test-results/native.json
The binary must be built with MyGo's e2e automation plugin and inspector.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import traceback

from smoke import RPC, screen_text, wait_until


class Automation:
    def __init__(self, path, pid=None):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(5)
        self.sock.connect(str(path))
        self.file = self.sock.makefile('rb')
        self.seq = 0
        self.pid = pid

    def ensure_foreground(self):
        if self.pid is None:
            return
        if any(w.get('focused') for w in self.call('windows')['windows']):
            return
        # Native keyboard events require a key window. Activate only this
        # test process through AppKit; do not synthesize a SurfaceFocus event.
        script = '''ObjC.import('AppKit');
function run(argv) {
    const app = $.NSRunningApplication.runningApplicationWithProcessIdentifier(Number(argv[0]));
    if (!app.activateWithOptions(2)) throw new Error('Cannot activate the test application');
}'''
        subprocess.run(['/usr/bin/osascript', '-l', 'JavaScript', '-e', script, str(self.pid)],
                       check=True, capture_output=True, text=True, timeout=5)
        wait_until(lambda: any(w.get('focused') for w in self.call('windows')['windows']),
                   'test window foreground', timeout=5)

    def call(self, method, **params):
        if method == 'input' or method == 'act' and params.get('action') == 'focus':
            self.ensure_foreground()
        self.seq += 1
        request = {'id': self.seq, 'method': method, 'params': params}
        self.sock.sendall((json.dumps(request, ensure_ascii=False) + '\n').encode())
        line = self.file.readline(32 << 20)
        if not line:
            raise RuntimeError('automation disconnected')
        response = json.loads(line)
        if 'error' in response:
            raise RuntimeError(f'{method}: {response["error"]}')
        return response['result']

    def nodes(self):
        # MyGo can hold native frames when a window is occluded. Capture
        # advances the held frame before reading the accessibility tree.
        self.call('capture')
        return self.call('tree')['nodes']

    def node(self, label, role=None, index=0):
        found = [n for n in self.nodes() if n.get('label') == label and (role is None or n.get('role') == role)]
        if len(found) <= index:
            raise AssertionError(f'cannot find {role or "element"} {label!r} #{index}')
        return found[index]

    def press(self, label, role='button', index=0):
        self.call('act', id=self.node(label, role, index)['id'], action='press')
        time.sleep(.15)

    def press_menu_item(self, label):
        nodes = [n for n in self.nodes() if n.get('label') == label and n.get('role') in ('menuItem', 'menuItemCheckBox', 'menuItemRadio')]
        assert len(nodes) == 1, f'cannot find unique menu item {label!r}'
        self.call('act', id=nodes[0]['id'], action='press')
        time.sleep(.15)

    def set(self, label, value):
        self.call('act', id=self.node(label, 'textField')['id'], action='setValue', text=value)
        time.sleep(.1)

    def input(self, *events):
        self.call('input', events=list(events))
        time.sleep(.15)

    def click_node(self, label, role='text'):
        b = self.node(label, role)['bounds']
        self.input({'type': 'click', 'x': b['x'] + min(30, b['width']/2), 'y': b['y'] + b['height']/2})

    def drag(self, x, y, dx, dy):
        self.input({'type':'pointerMove','x':x,'y':y}, {'type':'pointerDown','x':x,'y':y})
        for i in range(1, 6):
            self.input({'type':'pointerMove','x':x+dx*i/5,'y':y+dy*i/5})
        self.input({'type':'pointerUp','x':x+dx,'y':y+dy})

    def capture(self, path):
        image = self.call('capture')
        path.write_bytes(base64.b64decode(image['png']))
        return str(path)

    def close(self):
        self.file.close()
        self.sock.close()


def run(args):
    binary = Path(args.binary).resolve()
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    root = Path(tempfile.mkdtemp(prefix='wt-', dir='/tmp'))
    home, ws, ws2, sock = root/'h', root/'a', root/'b', root/'ui.sock'
    for p in (home, ws, ws2): p.mkdir()
    env = dict(os.environ, WTABLE_HOME=str(home), WTABLE_WORKSPACE=str(ws), MYGO_AUTOMATION=str(sock))
    report = {'status':'running', 'binary':str(binary), 'assertions':[], 'artifacts':[],
              'notes':['All UI actions use MyGo automation; daemon state and screen assertions use RPC.',
                       'Only isolated temporary home and workspaces are used.'], 'errors':[]}
    proc = None
    ui = None
    rpc = None
    app_log = (output.parent/'native-app.log').open('wb')

    def record(name, fn):
        try:
            details = fn()
            report['assertions'].append({'name':name, 'passed':True, 'details':details})
            print('PASS', name, flush=True)
            return True
        except Exception as exc:
            report['assertions'].append({'name':name, 'passed':False, 'error':str(exc)})
            report['errors'].append(traceback.format_exc())
            print('FAIL', name, str(exc), flush=True)
            try:
                report['artifacts'].append(ui.capture(output.parent/('native-failure-'+str(len(report['errors']))+'.png')))
            except Exception:
                pass
            return False

    def launch():
        nonlocal proc, ui
        proc = subprocess.Popen([str(binary)], env=env, cwd=str(ws), stdout=app_log, stderr=subprocess.STDOUT)
        wait_until(lambda: sock.exists() or proc.poll() is not None, 'automation socket', timeout=15)
        assert proc.poll() is None, 'app exited before automation started'
        ui = Automation(sock, proc.pid)
        wait_until(lambda: ui.call('windows')['windows'], 'native window', timeout=15)
        wait_until(lambda: any(n.get('label') == '新增 Agent' for n in ui.nodes()), 'workbench ready', timeout=20)

    def state(workspace=None):
        params = {'frames':False, 'graph':True}
        if workspace is not None: params['workspace'] = str(workspace)
        return rpc.call('state.get', params)

    def pane(name, workspace=ws):
        return next((p for p in state(workspace)['panes'] or [] if p['name'] == name), None)

    def desktop():
        return json.loads((home/'desktop.json').read_text())

    def screenshot(name):
        report['artifacts'].append(ui.capture(output.parent/('native-'+name+'.png')))

    def new_agent(name):
        ui.press('新增 Agent')
        ui.set('Agent 名称', name)
        trigger = ui.node('选择 Agent 底座', 'popUpButton')
        ui.call('act', id=trigger['id'], action='press')
        ui.click_node('Shell · 终端')
        assert ui.node('选择 Agent 底座', 'popUpButton').get('value') == 'shell'
        ui.press('高级设置')
        assert not ui.node('账号配置', 'textField').get('value', ''), 'fresh home must not assume a named account'
        ui.press('创建 Agent')
        wait_until(lambda: pane(name), 'created '+name, timeout=15)
        wait_until(lambda: any(n.get('label') == name and n.get('role') == 'button' for n in ui.nodes()), 'created UI '+name, timeout=15)
        return pane(name)

    def term_input(name, marker):
        def focus_terminal():
            nodes = [n for n in ui.nodes() if n.get('label') == 'Terminal']
            if not nodes:
                return False
            n = nodes[0]
            if 'focus' in n.get('actions', []):
                ui.call('act', id=n['id'], action='focus')
            else:
                b = n['bounds']
                ui.input({'type':'click','x':b['x']+80,'y':b['y']+70})
            return ui.call('tree').get('focus') == n['id']
        wait_until(focus_terminal, 'terminal receives focus', timeout=5)
        ui.input({'type':'text','text':'echo '+marker})
        ui.input({'type':'key','key':'Enter'})
        wait_until(lambda: marker in screen_text(rpc.call('pane.screen', {'id':pane(name)['id']})), 'terminal echo '+name, timeout=8)

    def shell_marker():
        term_input('测试 Alpha', 'WTABLE_NATIVE_FIRST_4096')
        return 'UI text/Enter and daemon pane.screen matched'

    def modal_focus_and_toast():
        pane_id = pane('测试 Alpha')['id']
        def grid():
            frame = rpc.call('pane.screen', {'id': pane_id})
            return frame['cols'], frame['rows']
        def terminal_bounds():
            return ui.node('Terminal', 'group')['bounds']
        wait_until(lambda: any(n.get('label') == 'Terminal' for n in ui.nodes()), 'terminal ready')
        before_grid, before_bounds = grid(), terminal_bounds()
        ui.press('新增 Agent')
        wait_until(lambda: any(n.get('label') == 'Agent 名称' for n in ui.nodes()), 'agent dialog')
        try:
            # A modal correctly hides background nodes from the accessibility
            # tree; the daemon remains the source of truth for its PTY grid.
            assert grid() == before_grid, 'agent dialog resized terminal grid'
        finally:
            ui.input({'type':'key','key':'Escape'})
        wait_until(lambda: not any(n.get('label') == 'Agent 名称' for n in ui.nodes()), 'dialog closed')
        terminal = ui.node('Terminal', 'group')
        focus = ui.call('tree').get('focus')
        ui.input({'type':'text','text':'echo WTABLE_NATIVE_ESC_4096'})
        ui.input({'type':'key','key':'Enter'})
        try:
            wait_until(lambda: 'WTABLE_NATIVE_ESC_4096' in screen_text(rpc.call('pane.screen', {'id':pane_id})), 'input after Esc', timeout=5)
        except AssertionError as exc:
            raise AssertionError(f'Esc did not restore input; terminal={terminal["id"]}, focus={focus}') from exc
        assert focus == terminal['id'], f'input worked but accessibility focus was {focus}, expected {terminal["id"]}'
        assert grid() == before_grid and terminal_bounds() == before_bounds, 'dialog close resized terminal'
        return 'dialog preserves terminal bounds and grid; Esc restores actual input focus'

    def search_agents():
        ui.set('搜索 Agent', 'Beta')
        wait_until(lambda: any(n.get('label') == 'Beta 第二' and n.get('role') == 'button' for n in ui.nodes()), 'search match')
        assert not any(n.get('label') == '测试 Alpha' and n.get('role') == 'button' for n in ui.nodes()), 'search did not filter Alpha'
        ui.press('清除搜索')
        wait_until(lambda: any(n.get('label') == '测试 Alpha' and n.get('role') == 'button' for n in ui.nodes()), 'clear search')
        return 'search filters the rail and clear restores the full list'

    def toast_layout(expected_grid):
        beta = pane('Beta 第二')['id']
        wait_until(lambda: any(n.get('label') == 'Agent 已创建' for n in ui.nodes()), 'creation toast', timeout=5)
        wait_until(lambda: any(n.get('label') == 'Terminal' for n in ui.nodes()), 'terminal ready')
        def beta_grid():
            frame = rpc.call('pane.screen', {'id':beta})
            return frame['cols'],frame['rows']
        # Both panes use the same Focus viewport. Wait for the newly attached
        # pane's first resize to match the already established previous grid.
        wait_until(lambda: beta_grid() == expected_grid, 'new terminal initial resize', timeout=5)
        assert any(n.get('label') == 'Agent 已创建' for n in ui.nodes()), 'toast expired before grid comparison'
        before = ui.node('Terminal', 'group')['bounds']
        frame = rpc.call('pane.screen', {'id':beta})
        before_grid = (frame['cols'],frame['rows'])
        wait_until(lambda: not any(n.get('label') == 'Agent 已创建' for n in ui.nodes()), 'toast dismissed', timeout=8)
        after = rpc.call('pane.screen', {'id':beta})
        assert ui.node('Terminal', 'group')['bounds'] == before, 'success toast changed terminal bounds'
        assert (after['cols'],after['rows']) == before_grid, f'success toast changed terminal grid: {before_grid} -> {(after["cols"],after["rows"])}'
        return {'before_grid':before_grid, 'after_grid':(after['cols'],after['rows']), 'bounds':before}

    def board_text(name):
        nodes = [n for n in ui.nodes() if n.get('label') == name and n.get('role') == 'text' and n.get('bounds', {}).get('x', 0) >= 232]
        assert nodes, 'board node not found: '+name
        return nodes[-1]

    def board_select(name):
        b = board_text(name)['bounds']
        ui.input({'type':'click','x':b['x']+min(30,b['width']/2),'y':b['y']+b['height']/2})

    def expand_preview_agent(name):
        if any(n.get('label') == name+'工作目录' for n in ui.nodes()):
            return
        width = ui.call('windows')['windows'][0]['contentBounds']['width']
        options = [n for n in ui.nodes() if n.get('role') == 'button' and n.get('label') == name
                   and n.get('bounds', {}).get('x', 0) >= width - 432]
        assert len(options) == 1, 'template agent control not found: '+name
        ui.call('act', id=options[0]['id'], action='press')
        wait_until(lambda: any(n.get('label') == name+'工作目录' for n in ui.nodes()), 'template agent details')

    def board():
        ui.press('Board')
        for name in ('测试 Alpha', 'Beta 第二'):
            b = board_text(name)['bounds']
            assert 232 <= b['x'] < 1440 and 44 <= b['y'] < 900, 'node outside visible board: '+name
        screenshot('board')
        ui.press('适应全部')
        before = desktop()['workspaces'][str(ws)]['viewport']
        ui.press('放大')
        wait_until(lambda: desktop()['workspaces'][str(ws)]['viewport']['Scale'] != before['Scale'], 'board zoom')
        prev = desktop()['workspaces'][str(ws)]['viewport'].copy()
        ui.drag(950, 750, -55, -40)
        wait_until(lambda: desktop()['workspaces'][str(ws)]['viewport'] != prev, 'board pan')
        ui.press('适应全部')
        n = board_text('测试 Alpha'); b = n['bounds']
        old = desktop()['workspaces'][str(ws)]['layouts'][pane('测试 Alpha')['id']].copy()
        ui.drag(b['x']+30, b['y']+5, 40, 35)
        wait_until(lambda: desktop()['workspaces'][str(ws)]['layouts'][pane('测试 Alpha')['id']] != old, 'node drag')
        ui.press('适应全部')
        ui.press('展开', index=0)
        ui.press('适应全部')
        resize = ui.node('调整节点尺寸', 'group')['bounds']
        old = desktop()['workspaces'][str(ws)]['layouts'][pane('测试 Alpha')['id']].copy()
        ui.drag(resize['x']+resize['width']/2, resize['y']+resize['height']/2, 50, 40)
        wait_until(lambda: desktop()['workspaces'][str(ws)]['layouts'][pane('测试 Alpha')['id']] != old, 'node resize')
        ui.press('展开')
        ui.press('适应全部')
        wait_until(lambda: len([n for n in ui.nodes() if n.get('label') == 'Terminal']) == 2, 'two expanded Board terminals')
        screenshot('board-expanded')
        return 'zoom, pan, node drag and resize changed persisted state; two Board terminals expanded'

    def dock():
        ui.press('Board')
        ui.press('适应全部')
        ui.press('Focus')
        ui.press('辅助终端', role='menuButton')
        ui.press_menu_item('测试 Alpha')
        wait_until(lambda: len([n for n in ui.nodes() if n.get('label') == 'Terminal']) == 2, 'two focus terminals')
        term_input('Beta 第二', 'WTABLE_NATIVE_DOCK_4096')
        assert 'WTABLE_NATIVE_DOCK_4096' not in screen_text(rpc.call('pane.screen', {'id':pane('测试 Alpha')['id']}))
        screenshot('focus')
        return 'two native terminals visible; active Beta receives input while silent Alpha remains independent'

    def edges_and_template():
        ui.press('Board'); ui.press('适应全部')
        board_select('测试 Alpha'); ui.press('授权'); board_select('Beta 第二')
        ui.press('测试 Alpha → Beta 第二', role='checkBox')
        alpha, beta = pane('测试 Alpha')['id'], pane('Beta 第二')['id']
        wait_until(lambda: any(e['from']==alpha and e['to']==beta for e in state(ws)['edges']), 'directed edge')
        assert not any(e['from']==beta and e['to']==alpha for e in state(ws)['edges'])
        ui.input({'type':'key','key':'CmdOrCtrl+T'})
        ui.press('保存当前拓扑为模板')
        ui.set('模板名称', 'Native E2E 模板')
        ui.press('保存为全局模板')
        wait_until(lambda: desktop().get('templates'), 'saved template')
        template = desktop()['templates'][0]
        assert len(template['Links']) == 1
        # A bad draft must be rejected before replacing the source workspace.
        original = {p['id'] for p in state(ws)['panes']}
        ui.press('预览并配置')
        expand_preview_agent('测试 Alpha')
        ui.set('测试 Alpha工作目录', str(root/'missing'))
        apply = ui.node('应用模板…', 'button')
        if 'scrollIntoView' in apply.get('actions', []): ui.call('act', id=apply['id'], action='scrollIntoView')
        ui.press('应用模板…'); ui.press('应用模板')
        wait_until(lambda: any('工作目录无效' in n.get('label', '') for n in ui.nodes()), 'invalid template rejection')
        assert original == {p['id'] for p in state(ws)['panes'] if p['state'] == 'running'}, 'invalid template changed running source panes'
        ui.press('关闭配置')
        ui.press('新建工作区')
        ui.set('项目目录', str(ws2))
        ui.press('创建工作区')
        wait_until(lambda: any(n.get('label') == ws2.name for n in ui.nodes()), 'second workspace')
        ui.input({'type':'key','key':'CmdOrCtrl+T'})
        ui.press('预览并配置')
        assert any(n.get('label') == '预览：Native E2E 模板' for n in ui.nodes())
        ui.press('适应全部')
        width = ui.call('windows')['windows'][0]['contentBounds']['width']
        inspector_left = width - 12 - 420
        fit = ui.node('适应全部', 'button')['bounds']
        assert fit['x'] + fit['width'] <= inspector_left, 'Board controls overlap template inspector'
        for name in ('测试 Alpha', 'Beta 第二'):
            visible = [n for n in ui.nodes() if n.get('role') == 'text' and n.get('label') == name
                       and 232 <= n['bounds']['x'] and n['bounds']['x'] + n['bounds']['width'] <= inspector_left]
            assert visible, 'template preview node obscured by inspector: '+name
        screenshot('template-preview')
        for name in ('测试 Alpha','Beta 第二'):
            expand_preview_agent(name)
            ui.set(name+'工作目录', str(ws2))
        # Preview is long; scroll to the apply button before invoking it.
        apply = ui.node('应用模板…', 'button')
        if 'scrollIntoView' in apply.get('actions', []): ui.call('act', id=apply['id'], action='scrollIntoView')
        ui.press('应用模板…'); ui.press('应用模板')
        wait_until(lambda: len(state(ws2)['panes'] or []) == 2, 'template applied', timeout=20)
        new = {p['name']:p for p in state(ws2)['panes']}
        edges = state(ws2)['edges']
        assert {(e['from'], e['to']) for e in edges} == {(new['测试 Alpha']['id'], new['Beta 第二']['id'])}
        saved = desktop()['workspaces'][str(ws2)]['layouts']
        by_key = {a['Key']:a for a in template['Agents']}
        for a in by_key.values():
            assert saved[new[a['Name']]['id']] == a['Layout'], 'template layout mismatch'
        screenshot('template-applied')
        return 'invalid draft preserves running panes; one-way edge and layouts retained in new workspace'

    def close_app():
        nonlocal ui, proc
        try:
            ui.call('menu', path=['wtable', 'Quit wtable'])
        except Exception:
            ui.input({'type':'key','key':'CmdOrCtrl+Q'})
        ui.close(); ui = None
        proc.wait(timeout=8)

    try:
        launch()
        token = wait_until(lambda: (home/'controller.token').exists() and (home/'controller.token').read_text().strip(), 'controller token')
        rpc = RPC(home, token)
        def native():
            windows = ui.call('windows')['windows']
            assert windows and all(w['kind']=='content' for w in windows), windows
            assert not any(n.get('role') == 'webView' for n in ui.nodes())
            screenshot('initial')
            return 'native content window; no webView node'
        record('native window', native)
        if record('create first shell with default account', lambda: new_agent('测试 Alpha')):
            record('terminal input and daemon screen', shell_marker)
            record('modal layout and focus restoration', modal_focus_and_toast)
            first_frame = rpc.call('pane.screen', {'id':pane('测试 Alpha')['id']})
            established_grid = (first_frame['cols'],first_frame['rows'])
            if record('create second shell with default account', lambda: new_agent('Beta 第二')):
                record('success toast preserves terminal layout', lambda: toast_layout(established_grid))
                record('agent search and clear', search_agents)
                record('board gestures', board)
                record('focus auxiliary terminal', dock)
                record('directed authorization and template', edges_and_template)
        def restore():
            ids = {p['id'] for p in state()['panes'] or []}
            assert ids
            close_app()
            live = {p['id'] for p in state()['panes'] or [] if p['state'] == 'running'}
            assert ids <= live, 'panes did not remain running after UI close'
            launch()
            assert len(state()['panes'] or []) >= len(ids)
            wait_until(lambda: any('测试 Alpha' in n.get('label','') for n in ui.nodes()), 'restored history')
            screenshot('restored')
            return 'all pane IDs remained running across GUI close; names restored after relaunch'
        record('close and restore', restore)
    except Exception as exc:
        report['errors'].append(traceback.format_exc())
        print('ERROR', exc, flush=True)
    finally:
        if ui is not None:
            try: close_app()
            except Exception:
                ui.close(); ui = None
        if proc is not None and proc.poll() is None:
            proc.terminate()
            try: proc.wait(timeout=5)
            except subprocess.TimeoutExpired: proc.kill(); proc.wait()
        if rpc is not None: rpc.close()
        shutdown = subprocess.run([str(binary),'shutdown'], env=env, cwd=str(ws), capture_output=True, text=True, timeout=10)
        report['cleanup'] = {'shutdown_exit_code':shutdown.returncode}
        app_log.close()
        if shutdown.returncode == 0:
            shutil.rmtree(root)
        else:
            report['cleanup']['isolated_dir'] = str(root)
            report['errors'].append('test daemon shutdown failed: '+shutdown.stderr)
        report['status'] = 'passed' if report['assertions'] and all(a['passed'] for a in report['assertions']) and not report['errors'] else 'failed'
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
        print('REPORT', output, report['status'], flush=True)
    return 0 if report['status'] == 'passed' else 1


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--output', default='.dev/test-results/native.json')
    raise SystemExit(run(parser.parse_args()))
