# q343864794 — yanlış server-main zarfı, 5 Eylül 2026

Gerçek gönderen **probot-studio-astra ana thread'i**
`01a0711e-1b8b-76a2-954a-76f9e1a44ceb`. Bu, mesajın Studio hakkında olmasından
çıkarılmadı: çalıştırılan komutun thread kaydı, dönen kanal ID'si, kuyruk dosyası
ve alıcıdaki birebir inbound mesaj birlikte eşleşti. Bu olay Studio prod onayı değildir.

## Kanıt zinciri

| Kanıt | Kayıt |
| --- | --- |
| Gönderim tool çağrısı | Studio rollout satır 2074, `2026-09-05T13:57:57.468Z`, `call_iY4Qp2X4VqNtJVqIj8BCbAB4` |
| Çalıştırılan komut | Aynı rollout satır 2077, `13:57:58.381Z`, `exec-62f83b48-ebd6-42fb-bf36-2e84febefb72` |
| Gerçek çağrı | `bp msg server-main 'Tuna için SON TOPLU ONAY: ...'`; çağıran `[server-main]` yazmamış |
| Komut sonucu | `RESULT=queued CHANNEL=q343864794` |
| Kuyruk | `/srv/server-main/msgq/done/q343864794.json`, `from=server-main`, `to=server-main`, `status=delivered` |
| Alıcı | Root rollout satır 415, `13:58:47.236Z`, inbound user metni kuyruktaki metinle birebir aynı |
| Tamamlanma | Queue finished `13:58:47.482661Z` |

Gönderen rollout:
`/root/.codex/sessions/2026/09/05/rollout-2026-09-05T13-29-53-01a0711e-1b8b-76a2-954a-76f9e1a44ceb.jsonl`

Alıcı rollout:
`/root/.codex/sessions/2026/09/05/rollout-2026-09-05T14-40-23-01a0715e-a5c0-7171-adf3-c95343ce6d5b.jsonl`

Mesaj zarfı SHA-256:
`6b998f45b54e4e2fc42b8c5140c77701ade3172169efefeb74c8e85a4fe2fe44`

Makine-okunur kanıt, kaynak dosya hash'i ve ham kayıtlara bağlantılar:
`/srv/blueprint/dist/rollout-20260905-compact/q343864794-evidence.json`.
Orijinal kuyruk/transkript dosyaları değiştirilmedi veya silinmedi.

## Neden sürüyor?

Canlı `/usr/local/bin/bp` ve `/srv/blueprint/bp` hâlâ eski SHA
`75dfc29f5116975aebe82eb3435ba17772cf694391ccc1363cb49f157b26985c`.
Hazır/yayınlanmış aday SHA
`acbaf97020b42c64f8ae1557d52e2066d51d8882a9b4de69d3fd583329ab8d9a`.

Eski kod TMUX doluyken hedefsiz session yanıtını kesin kimlik kabul ediyor;
senderIdentity ayrıca `Fallback: server-main` kullanıyor. Yeni aday per-execution
thread kanıtı ve açık kayıt istiyor; ortak daemon'ın eski TMUX/env bilgisinden
veya cwd'den root kimliği üretmiyor. Tmux kimliği de gerçek süreç atası kontrolüyle
doğrulanıyor. İstemcinin doğru etiketi kayıt anında koyması gerekir; yalnız
dispatcher'ı değiştirmek önceden yanlış zarflanmış kuyruğu düzeltmez.

Ortak daemon PID'leri bu sandbox'ın `/proc` görünümünde yok. Bu yeni olayın çağrı
anına ait environ snapshot'ı bulunmadığından **TMUX dalı mı fallback dalı mı**
çalıştı ayrımı bu kayıtlarla kesinleştirilemez. Önceki olayda miras TMUX kanıtlanmıştı;
burada kesin olan, çıplak bp çağrısından yanlış zarfı eski CLI'ın üretmesidir.

## Regresyon ve yayın

`TestStudioApprovalIncidentCannotAcquireRecipientIdentity`, olayın ana vscode
thread'i ve root UUID'siyle online mesaj zarfını test eder. Aynı stale
TMUX/TMUX_PANE/AGENT ortamında doğrulanmış çağrı `probot-studio-astra`, kanıtsız
çağrı `probot-studio-astra?` olur (eşleşmeyen thread `codex?:<UUID>` kalır).
Force ve root'a `/compact` reddedilir. Model çağrısı
ve gerçek mesaj gönderimi yapılmaz. İlgili CLI/identity/book race regresyonları geçti;
log `/tmp/bp-q343864794-regression.log` ve paket içindeki kopyadadır.

Bu olay aynı zamanda güncel alıcı thread'ini kayıttan doğrular:
`01a0715e-a5c0-7171-adf3-c95343ce6d5b`. Yeni hata bildiriminin gerçek gönderim
tool çağrısı da bu thread'de satır 436/438'dedir. Salt-okunur host ön kontrolü
bu root ve Studio eşlemesiyle geçti:
`/srv/blueprint/dist/rollout-20260905-compact/preflight-20260905T142623.606597Z/status.json`.
Bu kontrol live book veya servis değişikliği yapmadı.

Uygulanacak kesin komut ve hedefler [host uygulama dosyasında](host-rollout-2026-09-05.md).
Bu oturum sistem hedeflerine yazamıyor/systemd bus'a erişemiyor; `bp msg server-main`
de msgq yazımında salt-okunur hatası veriyor. Teknik sınır aşılmadı. Hostta uygulama
ve sonrasında Studio'nun **normal, ayrı bir test mesajının** doğru thread/zarfla
teslimi henüz yapılmadı. Onay paketini yeniden göndermek doğrulama yöntemi değildir.
