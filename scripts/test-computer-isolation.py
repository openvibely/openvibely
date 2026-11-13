#!/usr/bin/env python3
"""Live isolation regression. Uses only a new disposable container/volume.

Usage: python3 scripts/test-computer-isolation.py IMAGE
Requires Docker; the image must be built from packaging/computer/Dockerfile.
"""
import json
import secrets
import socket
import base64
import subprocess
import sys
import time
import urllib.error
import urllib.request

image = sys.argv[1]
name = 'ov-isolation-test-' + secrets.token_hex(5)
volume = name + '-home'
token = secrets.token_hex(32)

def docker(*args):
    return subprocess.check_output(['docker', *args], text=True).strip()

try:
    docker('run', '--rm', '--mount', f'type=volume,src={volume},dst=/home/ubuntu',
           '--entrypoint', '/bin/bash', image, '-c',
           'mkdir -p /home/ubuntu/.desktops/task-a/home/Files; '
           'echo legacy > /home/ubuntu/.desktops/task-a/home/Files/legacy.txt; '
           'mkdir -p /home/ubuntu/.desktops/task-unstarted/home/Files; '
           'echo unstarted-secret > /home/ubuntu/.desktops/task-unstarted/home/Files/private.txt; '
           'ln /home/ubuntu/.desktops/task-a/home/Files/legacy.txt /home/ubuntu/.desktops/task-unstarted/home/Files/linked.txt; '
           'chown -R 1000:1000 /home/ubuntu/.desktops')
    docker('run', '-d', '--name', name, '--cap-add', 'SYS_ADMIN', '--cap-add', 'NET_ADMIN', '--device', '/dev/net/tun',
           '--security-opt', 'apparmor=unconfined', '--security-opt', 'no-new-privileges:true',
           '--shm-size', '1g', '-p', '127.0.0.1::8090', '--mount', f'type=volume,src={volume},dst=/home/ubuntu',
           '-e', 'OPENVIBELY_COMPUTER_TOKEN=' + token, '-e', 'TZ=America/New_York', image)
    port = json.loads(docker('inspect', name))[0]['NetworkSettings']['Ports']['8090/tcp'][0]['HostPort']
    base = 'http://127.0.0.1:' + port

    def request(path, data=None, method=None, authorized=True):
        headers = {'Authorization': 'Bearer ' + token} if authorized else {}
        if isinstance(data, dict):
            data = json.dumps(data).encode()
            headers['Content-Type'] = 'application/json'
        req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
        with urllib.request.urlopen(req, timeout=45) as response:
            return response.read()

    def ready(task=''):
        last = None
        for _ in range(20):
            try:
                return json.loads(request(('/desktops/' + task if task else '') + '/health'))
            except (OSError, ValueError) as error:
                last = error
                time.sleep(1)
        raise AssertionError(f'{task} not ready: {last}')

    def shell(task, command, background=False):
        result = json.loads(request('/desktops/' + task + '/shell', {
            'command': command, 'working_directory': 'Files', 'timeout_seconds': 20,
            'background': background}))
        if not background:
            assert result['exit_code'] == 0, result
        return result

    def browser_fixture(task, action):
        # The operator's test probe uses the same authenticated CDP proxy as OV.
        code = r"""
const {chromium}=require('/opt/browser/node_modules/playwright');
(async()=>{
 const [task,action]=process.argv.slice(1);
 const browser=await chromium.connectOverCDP('http://127.0.0.1:8090/desktops/'+task,
  {headers:{Authorization:'Bearer '+process.env.OPENVIBELY_COMPUTER_TOKEN}});
 const context=browser.contexts()[0];
 if(action==='seed')await context.addCookies([{name:'isolation-fixture',value:'test',domain:'fixture.example.test',path:'/',expires:Math.floor(Date.now()/1000)+3600}]);
 else {
  const found=(await context.cookies()).some(c=>c.name==='isolation-fixture');
  if(found!==(action==='present'))throw Error('Browser profile isolation/persistence failure');
 }
 await browser.close();
})().catch(e=>{console.error(e.message);process.exit(1)});
"""
        docker('exec', name, 'node', '-e', code, task, action)

    assert ready()['isolation'] == 1
    try:
        request('/health', authorized=False)
        raise AssertionError('Unauthenticated router access')
    except urllib.error.HTTPError as error:
        assert error.code == 403
    ready('task-a')
    ready('task-b')
    assert request('/desktops/task-a/files?path=Files/legacy.txt') == b'legacy\n'
    assert docker('exec', name, 'stat', '-c', '%h', '/home/ubuntu/.desktops/task-a/home/Files/legacy.txt') == '1'
    request('/desktops/task-b/files?path=Files/private.txt', b'B-private', 'PUT')
    checks = r'''
set -eu
id
[ "$(id -u)" -ne 0 ]
! env | grep -E 'OPENVIBELY_COMPUTER_TOKEN|XAUTHORITY|DISPLAY|DESKTOP_UID'
! cat /home/ubuntu/.desktops/task-b/home/Files/private.txt 2>/dev/null
! cat /home/ubuntu/.desktops/task-unstarted/home/Files/private.txt 2>/dev/null
! ls /home/ubuntu/.desktops/task-b/browser 2>/dev/null
! cat /tmp/ov-display-authority 2>/dev/null
! curl --max-time 2 -sf --unix-socket /run/ov-desktops/task-b/control.sock http://desktop/screen
! curl --max-time 2 -sf --unix-socket /run/ov-desktops/task-a/control.sock http://desktop/screen
! curl --max-time 2 -sf http://127.0.0.1:10001/json/version
! curl --max-time 2 -sf http://127.0.0.1:10004/
! curl --max-time 2 -sf http://127.0.0.1:10005/
! curl --max-time 2 -sf http://127.0.0.1:8090/health
! curl --max-time 2 -sf http://10.0.2.2:8090/health
! DISPLAY=:100 xdpyinfo >/dev/null 2>&1
! cat /proc/1/environ 2>/dev/null
! setpriv --reuid=0 /bin/true 2>/dev/null
[ "$(awk '/NoNewPrivs:/ {print $2}' /proc/self/status)" = 1 ]
[ "$(awk '/CapEff:/ {print $2}' /proc/self/status)" = 0000000000000000 ]
printf durable > result.txt
printf boundary-checks-passed
'''
    output = shell('task-a', checks)['output']
    assert 'boundary-checks-passed' in output, output
    print('PASS: shell/file/network/X11/credential boundaries', flush=True)
    shell('task-a', 'curl -fsS --max-time 15 https://example.com >/dev/null')
    request('/desktops/task-b/browser', {'action': 'navigate', 'url': 'https://example.com'})
    browser_fixture('task-a', 'seed')
    browser_fixture('task-b', 'absent')
    print('PASS: outbound shell/browser network and separate login profiles', flush=True)
    # Authorized control remains available while direct access is blocked.
    image_data = request('/desktops/task-b/screen')
    assert image_data.startswith(b'\x89PNG'), image_data[:100]
    request('/desktops/task-b/action', {'action': 'press_key', 'values': {'key': 'Escape'}})
    assert json.loads(request('/desktops/task-b/json/version'))['Browser']
    assert json.loads(request('/desktops/task-b/public-cdp/json/version'))['Browser']
    assert request('/desktops/task-a/files?path=Files/result.txt') == b'durable'
    for mode in ('view', 'control'):
        with socket.create_connection(('127.0.0.1', int(port)), timeout=10) as stream:
            key = base64.b64encode(secrets.token_bytes(16)).decode()
            handshake = (f'GET /desktops/task-b/rfb-{mode} HTTP/1.1\r\n'
                         f'Host: 127.0.0.1:{port}\r\nAuthorization: Bearer {token}\r\n'
                         f'X-OV-Viewer-ID: live-{mode}\r\nX-OV-Viewer-Idle-Seconds: 1800\r\n'
                         'Connection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\n'
                         f'Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Protocol: binary\r\n\r\n')
            stream.sendall(handshake.encode())
            received = b''
            while b'\r\n\r\n' not in received:
                chunk = stream.recv(4096)
                assert chunk, 'Viewer closed before upgrade'
                received += chunk
            assert received.startswith(b'HTTP/1.1 101'), received[:200]
            while b'RFB ' not in received:
                chunk = stream.recv(4096)
                assert chunk, 'Viewer closed before RFB greeting'
                received += chunk
            time.sleep(1.2)
            assert json.loads(request('/desktop-reap', {'idle_seconds': 1, 'candidates': ['task-b']})) == [], 'Open viewer must prevent idle shutdown'
    print('PASS: authorized screen/input/browser/files and both viewer WebSockets', flush=True)
    # A detached shell must not survive takeover's cancel-all operation.
    shell('task-a', "setsid bash -c 'while :; do echo tick >> escaped.txt; sleep .1; done' >/dev/null 2>&1 & sleep 1")
    request('/desktops/task-a/jobs/cancel', {})
    first = request('/desktops/task-a/files?path=Files/escaped.txt')
    time.sleep(.4)
    assert request('/desktops/task-a/files?path=Files/escaped.txt') == first
    print('PASS: cancel-all stops detached shell processes', flush=True)
    # Keep task B alive while stopping/restarting A; retained files survive.
    request('/desktops/task-a/', method='DELETE')
    ready('task-a')
    assert request('/desktops/task-a/files?path=Files/result.txt') == b'durable'
    browser_fixture('task-a', 'present')
    assert request('/desktops/task-b/screen').startswith(b'\x89PNG')
    docker('restart', '--time', '20', name)
    port = json.loads(docker('inspect', name))[0]['NetworkSettings']['Ports']['8090/tcp'][0]['HostPort']
    base = 'http://127.0.0.1:' + port
    ready()
    ready('task-a')
    assert request('/desktops/task-a/files?path=Files/result.txt') == b'durable'
    browser_fixture('task-a', 'present')
    print('PASS: task and container restart preserve files and browser logins', flush=True)
    # A short supervisor timeout exercises the same path as the app's minutes policy.
    def reap(seconds=1, candidates=None):
        return json.loads(request('/desktop-reap', {'idle_seconds': seconds, 'candidates': candidates if candidates is not None else ['task-a']}))
    time.sleep(1.2)
    assert reap(0) == [], 'Never must preserve desktop'
    assert reap(candidates=[]) == [], 'Protected task must stay running'
    assert reap() == [], 'Protection must reset the idle clock'
    shell('task-a', "setsid sleep 30 >/dev/null 2>&1 & sleep .1")
    time.sleep(1.2)
    assert reap() == [], 'Detached command must prevent shutdown'
    request('/desktops/task-a/jobs/cancel', {})
    job = shell('task-a', 'sleep 4', background=True)
    time.sleep(1.2)
    assert reap() == [], 'Background command must prevent shutdown'
    time.sleep(3.2)
    assert reap() == [], 'Newly finished command must receive its full idle interval'
    time.sleep(1.2)
    assert reap() == ['task-a'], 'Idle desktop must release its slot'
    assert 'task-a' not in json.loads(request('/desktop-sessions'))
    ready('task-a')
    assert request('/desktops/task-a/files?path=Files/result.txt') == b'durable'
    browser_fixture('task-a', 'present')
    print('PASS: idle shutdown, Never, protected work, background jobs and reopen persistence', flush=True)

except Exception:
    subprocess.run(['docker', 'logs', '--tail', '100', name])
    raise
finally:
    subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL)
    subprocess.run(['docker', 'volume', 'rm', volume], stdout=subprocess.DEVNULL)
