"""Verify served bundle identity after lifecycle operations during mixed load."""
import json
import http.cookiejar
import threading
import time
import urllib.request
import urllib.error


class LifecycleProbe:
    def __init__(self, host, token, deploy, destination, interval):
        self.host, self.token, self.deploy = host, token, deploy
        self.destination, self.interval = destination, interval
        self.stop = threading.Event()
        self.thread = threading.Thread(target=self.run, daemon=True)
        self.cycles = 0
        self.errors = []
        self.cookies = http.cookiejar.CookieJar()

    def request(self, path, post=False, timeout=30):
        request = urllib.request.Request(self.host + path, data=b'' if post else None, headers={
            'User-Agent': 'shinyhub-loadtest', 'Authorization': 'Bearer ' + self.token})
        self.cookies.add_cookie_header(request)
        cookie = request.get_header('Cookie', '')
        request.add_unredirected_header('Cookie', (cookie + '; ' if cookie else '') + 'shiny_session=' + self.token)
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                self.cookies.extract_cookies(response, request)
                return response.read().decode()
        except urllib.error.HTTPError as error:
            self.cookies.extract_cookies(error, request)
            raise

    def verify(self, version):
        # Start each operation without old-generation affinity, but preserve
        # the new assignment while polling readiness (as a browser does).
        self.cookies.clear()
        deadline = time.monotonic() + 15
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError(f'bundle {version} did not become ready within 15 seconds')
            try:
                actual = self.request('/app/lifecycle/version', timeout=remaining)
            except urllib.error.HTTPError as error:
                if error.code != 503:
                    raise
                # The proxy also uses 503 during worker startup.
                # Bound this to the same readiness deadline as the loading page.
                error.close()
                actual = '<div id="shinyhub-box">Starting</div>'
            if actual == str(version):
                return
            # Activation can be acknowledged before its first worker is ready. Retry only the explicit loading page;
            # a served stale version remains an immediate failure.
            if 'id="shinyhub-box"' not in actual:
                raise RuntimeError(f'expected bundle {version}, received {actual[:100]!r}')
            if time.monotonic() >= deadline:
                raise RuntimeError(f'bundle {version} did not become ready within 15 seconds')
            time.sleep(0.1)

    def cycle(self, version, record):
        with self.destination.with_suffix('.deploy.log').open('a') as log:
            self.deploy('lifecycle', self.token, log, version=version)
        self.verify(version)
        record('deploy', version)
        self.request('/api/apps/lifecycle/restart', post=True)
        self.verify(version)
        record('restart', version)
        self.request('/api/apps/lifecycle/sleep', post=True)
        self.verify(version)  # The request must wake the same published bundle.
        record('wake', version)

    def run(self):
        with self.destination.open('w') as log:
            def record(operation, version):
                log.write(json.dumps({'time': time.time(), 'operation': operation, 'version': version}) + '\n')
                log.flush()
            while not self.stop.is_set():
                started = time.monotonic()
                try:
                    self.cycle(self.cycles + 1, record)
                    self.cycles += 1
                except Exception as error:
                    self.errors.append(str(error))
                    record('error: ' + str(error), self.cycles + 1)
                    return
                self.stop.wait(max(0, self.interval - (time.monotonic() - started)))

    def start(self):
        self.thread.start()

    def close(self):
        self.stop.set()
        self.thread.join(timeout=300)
        if self.thread.is_alive():
            raise RuntimeError('lifecycle worker failed to stop')

    def result(self):
        return {'completed_cycles': self.cycles, 'errors': self.errors}
