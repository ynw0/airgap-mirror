import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, extname, resolve } from 'node:path'
import { chromium } from 'playwright'

// Exercise the production React application in a real browser. Only the Wails
// bridge is mocked: no requests are made to a live mirror or server.
const root = resolve('dist')
const mime = {'.html':'text/html', '.js':'text/javascript', '.css':'text/css', '.svg':'image/svg+xml'}
const server = createServer(async (req, res) => {
  try {
    const path = join(root, req.url === '/' ? 'index.html' : req.url.split('?')[0])
    const body = await readFile(path)
    res.writeHead(200, {'Content-Type': mime[extname(path)] || 'application/octet-stream'})
    res.end(body)
  } catch { res.writeHead(404); res.end() }
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const base = 'http://127.0.0.1:' + server.address().port
let browser
try {
  browser = await chromium.launch({headless:true})
  await mkdir('layout-screenshots', {recursive:true})
  for (const [width, height, scale, theme] of [
    [1600, 1000, 1, 'light'],
    [1280, 800, 1.25, 'light'],
    [1024, 768, 1.5, 'dark'],
    [760, 900, 1.5, 'light'],
    [540, 900, 1, 'dark'],
  ]) {
    const context = await browser.newContext({viewport:{width,height}, deviceScaleFactor:scale, colorScheme:theme})
    await context.addInitScript(() => {
      const source = {id:'debian',name:'debin bookworm-updates',type:'apt',provider:'apt-generic',upstreamUrl:'http://mirror.costar.com/apt/debin',publicUrl:'http://mirror.costar.com/apt/debin',enabled:true,config:{suite:'bookworm-updates',releaseMode:'inrelease'}}
      const capsule = {id:'114840ec-0000-0000-0000-000000001042',exportedAt:'2026-11-27T07:50:15Z',schemaVersion:1,sources:[{source,state:{sourceId:source.id,liveCursor:{kind:'',value:''},liveObjects:100,liveBytes:1000,catalogVersion:1,updatedAt:'2026-11-27T07:50:15Z'}}]}
      window.go = {main:{App:{
        Bootstrap:async()=>({workspaceRoot:'C:/airgap',capsules:[capsule]}),
        ListEpochs:async()=>[],
        ProbeSource:async(...args)=>{window.lastProbe=args;return {cursor:{kind:'apt-release-snapshot-sha256',value:'abc'},observedAt:'2026-11-27T07:50:15Z'}}
      }}}
      window.runtime = {EventsOn:()=>()=>{}}
    })
    const page = await context.newPage()
    const errors = []
    page.on('pageerror', err => errors.push(err.message))
    await page.goto(base)
    await page.getByRole('navigation', {name:'应用导航'}).getByRole('button',{name:/联网下载/}).click()
    await page.locator('#sync-upstream-url').waitFor()
    const select = page.getByRole('combobox', {name:'选择官方源或镜像站'})
    const mirror = 'https://mirrors.tuna.tsinghua.edu.cn/debian'
    await select.selectOption(mirror)
    assert.equal(await page.locator('#sync-upstream-url').inputValue(), mirror)
    const layout = await page.locator('.sync-selector').evaluate(card => {
      const rect = e => {const r=e.getBoundingClientRect();return {left:r.left,right:r.right,top:r.top,bottom:r.bottom,width:r.width,height:r.height}}
      const fields = [...card.querySelectorAll('.sync-source-fields select')].map(rect)
      const url = rect(card.querySelector('#sync-upstream-url'))
      const actions = [...card.querySelectorAll('.sync-check-buttons button')].map(button=>{
        const range=document.createRange();range.selectNodeContents(button)
        return {...rect(button), lines:range.getClientRects().length}
      })
      return {card:rect(card),fields,url,actions,scrollWidth:card.scrollWidth,clientWidth:card.clientWidth}
    })
    assert.ok(layout.scrollWidth <= layout.clientWidth + 1, 'card overflows at '+width)
    const [first, second] = layout.fields
    if (layout.card.width >= 650) assert.ok(Math.abs(first.top-second.top)<2, 'top fields not aligned at '+width)
    assert.ok(layout.url.top >= Math.max(first.bottom,second.bottom), 'URL must appear below both source fields at '+width)
    assert.ok(layout.url.width >= Math.min(380,layout.card.width-42), 'URL is too narrow at '+width)
    for (const button of layout.actions) {
      assert.equal(button.lines,1,'button wraps at '+width)
      assert.ok(button.top >= layout.url.bottom && button.right <= layout.card.right+1, 'action outside card at '+width)
    }
    if (layout.card.width >= 650) assert.ok(Math.abs(layout.actions[0].top-layout.actions[1].top)<2,'action buttons not in one row at '+width)
    await page.screenshot({path:'layout-screenshots/sync-'+width+'-'+theme+'.png',fullPage:true})
    await page.getByRole('button',{name:'查看上游状态',exact:true}).click()
    await page.waitForFunction(()=>window.lastProbe?.[2] === 'https://mirrors.tuna.tsinghua.edu.cn/debian')
    await page.locator('.sync-actions summary').click()
    assert.ok(await page.getByText('每批目标大小 (GiB)',{exact:true}).isVisible())
    assert.deepEqual(errors,[],'browser errors at '+width)
    await context.close()
    console.log('PASS '+width+'x'+height+' DPR '+scale+' '+theme, JSON.stringify(layout))
  }
} finally {
  await browser?.close()
  await new Promise(resolve=>server.close(resolve))
}
