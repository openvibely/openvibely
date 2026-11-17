#!/usr/bin/env python3
"""Shared-user regression against a new disposable computer container and volume.
Usage: python3 scripts/test-computer-shared-user.py IMAGE
"""
import hashlib
import json
import secrets
import shlex
import subprocess
import sys
import time
import urllib.request

image = sys.argv[1]
name = 'ov-shared-user-test-' + secrets.token_hex(5)
volume = name + '-home'
token = secrets.token_hex(32)
uid = 20000 + (int.from_bytes(hashlib.sha256(b'task-a').digest()[:4], 'big') % 500000000) * 2

def docker(*args):
    return subprocess.check_output(['docker', *args], text=True).strip()

try:
    docker('run', '--rm', '--mount', f'type=volume,src={volume},dst=/home/ubuntu',
           '--entrypoint', '/bin/bash', image, '-c',
           'mkdir -p /home/ubuntu/.desktops/task-a/home/Files; '
           'printf retained > /home/ubuntu/.desktops/task-a/home/Files/old.txt; '
           f'chown {uid+1}:{uid+1} /home/ubuntu/.desktops/task-a/home/Files/old.txt; '
           'chmod 0600 /home/ubuntu/.desktops/task-a/home/Files/old.txt')
    docker('run', '-d', '--name', name, '--cap-add', 'SYS_ADMIN', '--cap-add', 'NET_ADMIN',
           '--device', '/dev/net/tun', '--security-opt', 'apparmor=unconfined',
           '--security-opt', 'no-new-privileges:true', '--shm-size', '1g',
           '-p', '127.0.0.1::8090', '--mount', f'type=volume,src={volume},dst=/home/ubuntu',
           '-e', 'OPENVIBELY_COMPUTER_TOKEN=' + token, image)
    port = json.loads(docker('inspect', name))[0]['NetworkSettings']['Ports']['8090/tcp'][0]['HostPort']
    base = 'http://127.0.0.1:' + port

    def request(path, data=None):
        headers = {'Authorization': 'Bearer ' + token}
        if isinstance(data, dict):
            data = json.dumps(data).encode()
            headers['Content-Type'] = 'application/json'
        with urllib.request.urlopen(urllib.request.Request(base + path, data=data, headers=headers), timeout=45) as response:
            return json.load(response)

    def ready(task):
        for _ in range(25):
            try:
                return request('/desktops/' + task + '/health')
            except OSError:
                time.sleep(1)
        raise AssertionError('desktop did not start: ' + task)

    def shell(task, command, background=False):
        result = request('/desktops/' + task + '/process-tools', {
            'execution_id': 'shared-user-test', 'name': 'shell',
            'input': {'command': command, 'timeout': 120}})
        assert not result.get('error'), result
        output = result['output']
        if not background:
            assert 'exit_code: 0' in output, output
        return output

    ready('task-a')
    ready('task-b')
    # Verify the harness rejects an earlier failed assertion even if a later
    # command would succeed. This catches accidental semicolon chaining.
    try:
        shell('task-a', 'test 1 = 2 && printf should-not-run')
    except AssertionError:
        pass
    else:
        raise AssertionError('shell assertion failure was ignored')
    result = shell('task-a', 'id -u && cat old.txt && test "$(stat -c %u old.txt)" = "$(id -u)"')
    assert str(uid) in result and 'retained' in result, result
    shell('task-b', 'umask 077; printf private > private.txt')
    for check in (
        '! cat /home/ubuntu/.desktops/task-b/home/Files/private.txt',
        '! cat /proc/1/environ',
        '! curl -fsS --max-time 2 --unix-socket /run/ov-desktops/task-a/control.sock http://desktop/health',
    ):
        shell('task-a', check)
    print('PASS: retained ownership migration and cross-task/root-control isolation', flush=True)

    shell('task-a', "printf '<a href=report.txt>Report</a>' > index.html && printf downloaded > report.txt")
    shell('task-a', 'exec python3 -m http.server 18765 --bind 127.0.0.1', background=True)
    prefix = '/desktops/task-a/browser'
    assert 'error' not in request(prefix, {'action': 'navigate', 'url': 'http://127.0.0.1:18765/'})
    download = request(prefix, {'action': 'download_media', 'locator': {'text': 'Report'}})
    path = shlex.quote(download['computer_path'])
    output = shell('task-a', f'cat {path} && test "$(stat -c %u {path})" = "$(id -u)" && printf edited >> {path}')
    assert 'downloaded' in output, output
    print('PASS: actual browser download is readable and editable by shell under same UID', flush=True)

    request('/desktops/task-a/jobs/cancel', {})
    screenshot = request(prefix, {'action': 'screenshot'})
    assert screenshot['content'][0]['type'] == 'image', screenshot
    print('PASS: shell cancellation preserves Chromium and browser worker', flush=True)
    docker('restart', '--time', '20', name)
    port = json.loads(docker('inspect', name))[0]['NetworkSettings']['Ports']['8090/tcp'][0]['HostPort']
    base = 'http://127.0.0.1:' + port
    ready('task-a')
    assert 'downloadededited' in shell('task-a', f'cat {path}')
    print('PASS: shared files survive container restart', flush=True)
except Exception:
    subprocess.run(['docker', 'logs', '--tail', '30', name])
    raise
finally:
    subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL)
    subprocess.run(['docker', 'volume', 'rm', volume], stdout=subprocess.DEVNULL)
