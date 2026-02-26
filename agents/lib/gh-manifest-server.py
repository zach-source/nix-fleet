#!/usr/bin/env python3
"""GitHub App manifest flow callback server.

Starts a local HTTP server that:
1. Serves a form page that auto-submits the app manifest to GitHub
2. Catches the callback with the authorization code
3. Exchanges the code for app credentials (id, pem, client_id, etc.)
4. Writes credentials to a JSON file and exits

Usage:
  python3 gh-manifest-server.py <manifest_json> <org> <output_file> [port]
"""
import http.server
import json
import sys
import threading
import urllib.parse
import urllib.request

PORT = int(sys.argv[4]) if len(sys.argv) > 4 else 3141
MANIFEST = sys.argv[1]
ORG = sys.argv[2]
OUTPUT_FILE = sys.argv[3]


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        params = urllib.parse.parse_qs(parsed.query)

        if parsed.path == "/":
            # Serve auto-submit form
            # HTML-escape the manifest for the hidden input value
            manifest_escaped = MANIFEST.replace("&", "&amp;").replace('"', "&quot;")
            html = f"""<!DOCTYPE html>
<html><body>
<form id="f" method="post" action="https://github.com/organizations/{ORG}/settings/apps/new">
  <input type="hidden" name="manifest" value="{manifest_escaped}">
  <p>Redirecting to GitHub to create app...</p>
  <noscript><button type="submit">Click to continue</button></noscript>
</form>
<script>document.getElementById('f').submit()</script>
</body></html>"""
            self.send_response(200)
            self.send_header("Content-Type", "text/html")
            self.end_headers()
            self.wfile.write(html.encode())

        elif parsed.path == "/callback":
            code = params.get("code", [None])[0]
            if not code:
                self.send_response(400)
                self.end_headers()
                self.wfile.write(b"No code received")
                return

            # Exchange code for app credentials
            try:
                req = urllib.request.Request(
                    f"https://api.github.com/app-manifests/{code}/conversions",
                    method="POST",
                    headers={
                        "Accept": "application/vnd.github+json",
                    },
                    data=b"",
                )
                resp = urllib.request.urlopen(req)
                data = json.loads(resp.read())
            except Exception as e:
                self.send_response(500)
                self.end_headers()
                self.wfile.write(f"Error exchanging code: {e}".encode())
                return

            # Extract credentials
            result = {
                "id": data["id"],
                "slug": data.get("slug", ""),
                "name": data["name"],
                "client_id": data["client_id"],
                "client_secret": data["client_secret"],
                "pem": data["pem"],
                "webhook_secret": data.get("webhook_secret", ""),
                "owner": data.get("owner", {}).get("login", ""),
                "html_url": data.get("html_url", ""),
            }

            with open(OUTPUT_FILE, "w") as f:
                json.dump(result, f, indent=2)

            self.send_response(200)
            self.send_header("Content-Type", "text/html")
            self.end_headers()
            self.wfile.write(
                f"""<!DOCTYPE html>
<html><body>
<h2>GitHub App created successfully</h2>
<p><strong>Name:</strong> {result['name']}</p>
<p><strong>App ID:</strong> {result['id']}</p>
<p><strong>Slug:</strong> {result['slug']}</p>
<p>You can close this tab and return to the terminal.</p>
</body></html>""".encode()
            )

            # Shutdown server after response is sent
            threading.Thread(target=self.server.shutdown, daemon=True).start()

        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, format, *args):
        pass  # Suppress request logs


if __name__ == "__main__":
    server = http.server.HTTPServer(("127.0.0.1", PORT), Handler)
    print(f"Listening on http://127.0.0.1:{PORT}", flush=True)
    print(f"Open http://127.0.0.1:{PORT} in your browser to start", flush=True)
    server.serve_forever()
