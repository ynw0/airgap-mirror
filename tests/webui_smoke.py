"""Browser smoke test using the actual embedded HTML and a deterministic mock API."""
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from playwright.sync_api import sync_playwright, expect

ROOT = Path(__file__).resolve().parents[1]
HTML = (ROOT / "internal/server/web/index.html").read_bytes()
PREFIX = "/mirror-control"
DATE = "2026-11-25T00:00:00Z"
SOURCES = [
    dict(id="pypi", name="PyPI 主仓库", type="pypi", provider="pypi",
         rootPath="/srv/mirror/pypi", publicUrl="https://mirror.example/pypi",
         upstreamUrl="https://pypi.org/simple", enabled=True, config={}),
    dict(id="apt", name="Debian Bookworm", type="apt", provider="apt-generic",
         rootPath="/srv/mirror/debian", publicUrl="https://mirror.example/debian",
         upstreamUrl="https://deb.debian.org/debian", enabled=True,
         config=dict(suite="bookworm", releaseMode="inrelease")),
]
TASKS = [dict(id="report", sourceId="pypi", kind="REPAIR", status="COMPLETED",
              hashMiBPerSecond=64, scannedObjects=7800000,
              scannedBytes=32*1024**4, candidateObjects=2,
              createdAt=DATE, completedAt=DATE)]
EXPORTS = []
REQUESTS = []

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, value, status=200):
        data = json.dumps(value, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self.dispatch()

    def do_POST(self):
        self.dispatch()

    def dispatch(self):
        from urllib.parse import urlsplit, parse_qs
        u = urlsplit(self.path)
        if u.path in ("/", PREFIX+"/"):
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'")
            self.end_headers()
            self.wfile.write(HTML)
            return
        if not u.path.startswith(PREFIX+"/api/v1/"):
            self.reply({"error":"wrong proxy prefix"}, 404)
            return
        REQUESTS.append((self.command, u.path))
        if self.headers.get("Authorization") != "Bearer smoke-token":
            self.reply({"error":"invalid token"}, 401)
            return
        p = u.path[len(PREFIX):]
        q = parse_qs(u.query)
        sid = q.get("sourceId", [""])[0]
        if self.command == "POST":
            n = int(self.headers.get("Content-Length", "0"))
            body = json.loads(self.rfile.read(n) or b"{}")
            if p.endswith("/incremental-inventory"):
                j = dict(id="inventory", sourceId="pypi", kind="INVENTORY", incrementalInventory=True, status="RUNNING", createdAt=DATE, completedProjects=250000,totalProjects=650000,scannedObjects=3100000,scannedBytes=12*1024**4,lastProject="numpy")
                TASKS.insert(0,j)
                self.reply(j,202)
            elif p.endswith("/pause"):
                TASKS[0]["status"]="PAUSED"
                self.reply(TASKS[0],202)
            elif p.endswith("/resume"):
                TASKS[0]["status"]="RUNNING"
                self.reply(TASKS[0],202)
            elif p.endswith("/cancel"):
                TASKS[0]["status"]="CANCELLED"
                self.reply(TASKS[0],202)
            elif p.endswith("/repair-approval"):
                TASKS[-1]["replacementApproved"]=body["approve"]
                self.reply({"approved":body["approve"]})
            elif p.endswith("/export-jobs"):
                j=dict(id="export-1",status="COMPLETED",stage="完成",completedSources=len(body["sourceIds"]),totalSources=len(body["sourceIds"]),createdAt=DATE,name="mirror-state-export-1.mstate",download="/api/v1/state/exports/mirror-state-export-1.mstate")
                EXPORTS.append(j)
                self.reply(j,202)
            else:
                self.reply({"error":"unsupported test action"},404)
            return
        if p == "/api/v1/sources":
            self.reply(SOURCES)
        elif p.endswith("/state"):
            is_pypi = "/pypi/" in p
            self.reply(dict(liveObjects=7800000 if is_pypi else 380000,
                            liveBytes=(32 if is_pypi else 2)*1024**4,
                            liveCursor=dict(kind="pypi-serial" if is_pypi else "apt-release",value="12345")))
        elif p == "/api/v1/capacity":
            self.reply(dict(totalBytes=60*1024**4,freeBytes=20*1024**4))
        elif p == "/api/v1/maintenance":
            self.reply([j for j in TASKS if j["sourceId"]==sid])
        elif p.endswith("/repair-issues"):
            self.reply(dict(items=[dict(kind="HASH_MISMATCH",logicalPath="packages/demo.whl",sha256="a"*64,actualSha256="b"*64)],next=""))
        elif p == "/api/v1/state/export-jobs":
            self.reply(EXPORTS)
        elif p == "/api/v1/epochs":
            self.reply([dict(id="epoch-1",sourceId=sid,status="COMPLETE",createdAt=DATE,totalBytes=1024**3,totalObjects=100,totalBatches=1)])
        elif p.endswith("/batches"):
            self.reply([dict(id="batch-1",sequence=1,status="IMPORTED",plannedBytes=1024**3,objectCount=100,packCount=1)])
        else:
            self.reply({"error":"unknown test route"},404)

def main():
    server=ThreadingHTTPServer(("127.0.0.1",0),Handler)
    threading.Thread(target=server.serve_forever,daemon=True).start()
    out=ROOT/"webui-preview"
    out.mkdir(exist_ok=True)
    try:
        with sync_playwright() as pw:
            browser=pw.chromium.launch()
            page=browser.new_page(viewport={"width":1440,"height":1000},device_scale_factor=1)
            errors=[]
            page.on("pageerror",lambda e:errors.append(str(e)))
            page.on("dialog",lambda d:d.accept())
            page.goto(f"http://127.0.0.1:{server.server_port}{PREFIX}/")
            expect(page.get_by_role("heading",name="连接内网服务器")).to_be_visible()
            page.screenshot(path=str(out/"login.png"),full_page=True)
            assert not REQUESTS, "protected API called before login"
            page.locator("#token").fill("wrong")
            page.locator("#connect").click()
            expect(page.locator("#loginError")).to_contain_text("连接失败")
            page.locator("#token").fill("smoke-token")
            page.locator("#connect").click()
            expect(page.get_by_role("heading",name="仓库总览")).to_be_visible()
            expect(page.locator("#overviewTable")).to_contain_text("PyPI 主仓库")
            page.screenshot(path=str(out/"overview.png"),full_page=True)
            page.locator('[data-page="repositories"]').click()
            page.locator("#sourceSearch").fill("Debian")
            expect(page.locator("#sources")).to_contain_text("Debian Bookworm")
            expect(page.locator("#sources")).not_to_contain_text("PyPI 主仓库")
            page.get_by_role("button",name="编辑设置").click()
            expect(page.locator("#config")).to_have_value(json.dumps(SOURCES[1]["config"],indent=2))
            page.locator("#cancelEdit").click()
            page.locator('[data-page="maintenance"]').click()
            page.locator("#maintSource").select_option("pypi")
            page.get_by_role("button",name="详情",exact=True).click()
            page.get_by_role("button",name="查看异常文件").click()
            expect(page.locator("#detailBody")).to_contain_text("SHA256 不符")
            page.get_by_role("button",name="授权损坏文件修复",exact=True).click()
            expect(page.locator("#detailDialog")).not_to_be_visible()
            page.get_by_role("button",name="增量清点",exact=True).click()
            expect(page.locator("#taskTable")).to_contain_text("增量清点")
            expect(page.get_by_role("button",name="增量清点",exact=True)).to_be_disabled()
            page.get_by_role("button",name="暂停",exact=True).click()
            expect(page.get_by_role("button",name="继续",exact=True)).to_be_visible()
            page.screenshot(path=str(out/"maintenance.png"),full_page=True)
            page.get_by_role("button",name="继续",exact=True).click()
            expect(page.get_by_role("button",name="暂停",exact=True)).to_be_visible()
            page.get_by_role("button",name="取消",exact=True).click()
            expect(page.get_by_role("button",name="增量清点",exact=True)).to_be_enabled()
            page.locator('[data-page="sync"]').click()
            page.locator("#syncSource").select_option("apt")
            expect(page.locator("#syncTable")).to_contain_text("epoch-1")
            page.get_by_role("button",name="查看批次").click()
            expect(page.locator("#batchTable")).to_contain_text("第 1 批")
            page.locator('[data-page="storage"]').click()
            expect(page.locator("#storageCards")).to_contain_text("20.0 TiB")
            page.locator('[data-page="exports"]').click()
            page.locator("#exportAll").click()
            expect(page.locator("#exportJobs")).to_contain_text("mirror-state-export-1.mstate")
            page.screenshot(path=str(out/"exports.png"),full_page=True)
            page.locator('[data-page="overview"]').click()
            page.set_viewport_size({"width":390,"height":844})
            page.screenshot(path=str(out/"mobile.png"),full_page=True)
            assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1"), "mobile page overflows"
            page.locator("#logout").click()
            expect(page.locator("#login")).to_be_visible()
            expect(page.locator("#app")).not_to_be_visible()
            assert page.locator("#sources").inner_text()=="", "source state retained after logout"
            assert not errors, errors
            assert all(p.startswith(PREFIX+"/api/v1/") for _,p in REQUESTS)
            browser.close()
    finally:
        server.shutdown()

if __name__=="__main__":
    main()
