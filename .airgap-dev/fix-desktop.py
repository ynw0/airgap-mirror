from pathlib import Path

app = Path('desktop/app.go')
s = app.read_text(encoding='utf-8')
old = '''func (a *App) ResumeEpoch(epochID string) (domain.Epoch, []domain.Batch, error) {
\tworkspace, err := a.workspaceRef()
\tif err != nil {
\t\treturn domain.Epoch{}, nil, err
\t}
\treturn workspace.ResumeEpoch(a.ctx, epochID)
}'''
new = '''func (a *App) ResumeEpoch(epochID string) (EpochView, error) {
\tworkspace, err := a.workspaceRef()
\tif err != nil {
\t\treturn EpochView{}, err
\t}
\tepoch, batches, err := workspace.ResumeEpoch(a.ctx, epochID)
\tif err != nil {
\t\treturn EpochView{}, err
\t}
\treturn EpochView{Epoch: epoch, Batches: batches}, nil
}'''
if old not in s:
    raise SystemExit('ResumeEpoch source shape changed')
app.write_text(s.replace(old, new, 1), encoding='utf-8')

page = Path('desktop/frontend/src/pages/InternetSyncPage.tsx')
s = page.read_text(encoding='utf-8')
old = '''  async function download(batch: Batch) {
    if (!destination) { const value = await app().PickDownloadDirectory(); if (!value) return; setDestination(value) }
    const target = destination || await app().PickDownloadDirectory(); if (!target) return
    setRunning(prev => new Set(prev).add(batch.id)); setMessage('')'''
new = '''  async function download(batch: Batch) {
    let target = destination
    if (!target) {
      target = await app().PickDownloadDirectory()
      if (!target) return
      setDestination(target)
    }
    setRunning(prev => new Set(prev).add(batch.id)); setMessage('')'''
if old not in s:
    raise SystemExit('InternetSync download source shape changed')
page.write_text(s.replace(old, new, 1), encoding='utf-8')
