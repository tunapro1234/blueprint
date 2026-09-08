# Server-main için host uygulama paketi

**Tamamlandı:** sandbox Tuna tarafından kaldırıldıktan sonra host kurulumu ve gerçek
mesaj teslimi doğrulandı. [Güncel canlı rapor](live-rollout-2026-09-05.md).
Aşağıdaki devir/engel bilgileri önceki aşamanın tarihsel kaydıdır.

Tuna'nın 5 Eylül açık operasyonel yetkisi var; yeniden onay beklenmiyor.
Bu belge teknik sandbox nedeniyle yetkili host kabuğuna devredilen işlemdir.
Blueprint kendi oturumunu kapatmadı veya sandbox'ı aşmadı.

## Kesin artifact ve hedefler

Paket: `/srv/blueprint/dist/rollout-20260905-compact/`

Linux amd64 binary SHA-256:
`acbaf97020b42c64f8ae1557d52e2066d51d8882a9b4de69d3fd583329ab8d9a`

| Paket dosyası | Host hedefi |
| --- | --- |
| `bp-linux-amd64` | `/srv/blueprint/bp` ve `/usr/local/bin/bp` — ayrı kopyalar |
| `bp_runtime_usage.py` | `/srv/server-main/bin/bp_runtime_usage.py` |
| `otonom-yakit-bekcisi.py` | `/srv/server-main/bin/otonom-yakit-bekcisi.py` |
| `migrate-codex-book.py` | `/srv/server-main/agentbook.json` ve `/srv/probot/.orchestration/agentbook.json` üzerinde kilitli/yedekli güncelleme |

`apply-host-rollout.py`, manifest doğrulaması, salt-okunur root runtime kontrolü,
yedekleme, atomik dosya değişimi, yalnız `blueprint.service` stop/start ve son
CLI/daemon SHA kontrolünü uygular. Bekçiyi `--kuru` çalıştırır; WhatsApp bildirimi
göndermez. İnceleme/kanıt/yedek dosyaları paket altında korunur.

## Güncel root thread'i

Güncelleme: [q343864794 olay zinciri](identity-incident-q343864794.md) gerçek Studio
gönderimini ve alıcı root thread'ini kayıttan eşleştirdi. Aşağıdaki yeni UUID'lerle
14:26 UTC salt-okunur ön kontrol geçti; `preflight-20260905T142623.606597Z/status.json`
kanıttır. Host uygulaması yapılmadı. Önceki yalnız cwd/başlık gözleminin yerine
artık komut sonucu/kanal/alıcı inbound eşleşmesi var.

Eski `01a0617e-29f9-79a3-be66-72ea1dec4718` için 14:11 UTC sorgusu `notLoaded`
döndü. Bu ID ile ön kontrol **beklendiği gibi reddedildi**, host değişikliği olmadı.
Eski CODEX-CUTOVER komutundaki ID'yi körlemesine uygulama.

14:07 UTC salt-okunur loaded/list + thread/read sonucunda görülen ADAYLAR:

- `/srv`, başlık `sen server-main agentsın onboarding`:
  `01a0715e-a5c0-7171-adf3-c95343ce6d5b`.
- `/srv/probot/studio/astra`, başlık `probot-studio-astra`:
  `01a0711e-1b8b-76a2-954a-76f9e1a44ceb`.

Bunlar yalnız cwd/başlık üzerinden yetki atamak için yeterli değildir.
Server-main kendi gerçek thread'ini, studio eşlemesini de gerçek istemci/thread
bağından doğrulamalı. Aynı cwd'deki CLI-içi subagentlara parent yetkisi verilmez.
Blueprint ve eğitim Astra için önceden açıkça bildirilmiş UUID'ler scriptte korunur.

Yukarıdaki iki aday gerçek oturumlarla doğrulandığında hostta uygulanacak komut:

```sh
python3 /srv/blueprint/dist/rollout-20260905-compact/apply-host-rollout.py \
  --bundle /srv/blueprint/dist/rollout-20260905-compact \
  --root-thread 01a0715e-a5c0-7171-adf3-c95343ce6d5b \
  --bind probot-studio-astra=01a0711e-1b8b-76a2-954a-76f9e1a44ceb \
  --apply
```

Gerçek ID farklıysa ilgili argümanı onunla değiştir; belirsizliği tahminle kapatma.
`--apply` olmadan aynı komut yalnız ön kontrol ve inceleme dosyalarını oluşturur.
Script root loaded/runtime kontrolünü geçmeden servis durdurmaz. Mevcut agent
konuşmalarını, root dışındaki launch ayarlarını, model/effort'u, Codex daemonunu ve
tmux seçeneklerini değiştirmez. Diğer eksik thread eşlemeleri aynı kanıt şartıyla
`--bind` olarak eklenebilir. Root için zaten onaylı remote/noSandbox tercihi korunur.

## Hosttan beklenen canlı doğrulama

1. Script sonunda producer ve daemon SHA aynı, `daemon_verification=verified executable`
   olmalı. `preflight-*/installed-status.json` kanıt dosyasıdır.
2. `bp bar probot-studio-astra` Codex kotasını göstermeli. Son usage örneğinde haftalık
   veri yoksa haftalık yüzde uydurulmaz; bulunan kısa dönem limit gösterilir.
3. op-main için son compact ölçümü, 12:07:03.748 UTC olayındaki **12222** token idi.
   Yeni assistant ölçümü geldiyse daha yeni değer geçerlidir; eski **361381** değerine
   dönmemeli. Runtime çalışma durumu bu sayıdan bağımsızdır.
4. Bekçinin kuru çalışması gerçek agent adları ve kapsam dışı/unknown ölçümleri
   raporlamalı. Codex ham tokenlarına Claude ağırlıklı alarm eşiği uygulanmaz.
5. Normal `bp msg blueprint 'Host kurulumu tamamlandı; manifest SHA ve daemon
   doğrulandı, kanıt dizini: ...'` gönderimi çalışma sırasında kuyruğa girmeli; force
   kullanma. Gerçek teslim, kanal kaydı **ve doğru thread'deki inbound mesaj** ile
   doğrulanmalı. Kuyruğa girmeyi veya composer'dan kaybolmayı teslim sayma.

Script yarıda durursa yedek dizinini ve her iki kitabı incele. Geri dönüş yalnız
Blueprint servisi, yedek binary/bekçi ve iki kitap setiyle yapılır; hiçbir kayıt silinmez.

## Gerçek kanıt ve kalan engel

- Tüm Go paketlerinde race testleri, go vet ve 17 Python testi geçti.
- Compact regresyonları eklendi; gerçek op-main transkriptiyle aday **12222** okudu.
- Yayınlanan CLI, gerçek ESC içeren mesajı kuyruk/paste öncesi reddetti.
- Public dört binary + installer + checksum yayınlandı; normal public URL'lerinden
  indirilerek SHA-256 karşılaştırıldı. Kanıt: `public-verification.json`.
- Eski public dosyalar `/srv/blueprint/dist/public-backup-20260905T141227Z/` altında.
- Host CLI ve daemon henüz güncellenmedi. `/usr/local/bin` mount'u salt okunur;
  `systemctl` bus erişimi yok. `bp msg server-main` de msgq yazımında salt-okunur
  hatası aldı; koordinasyon mesajının ulaştığı iddia edilmiyor.

Public kurulum komutu artık güncel dosyayı kullanır:

```sh
curl -fsSL https://bp.tunapro.xyz/install.sh | sh -s -- --local
```

Kalan iş host uygulaması, doğrulanmış thread kayıtları ve gerçek normal mesaj
teslim kanıtıdır. Yeni/pinsiz Codex oturumlarının unknown kalma sınırı
[runtime notunda](runtime-status.md) açıklanmıştır.
