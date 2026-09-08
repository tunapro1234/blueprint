# 5 Eylül: host kurulumu tamamlandı

Tuna'nın verdiği host erişimi, mevcut dosyaları değiştirmeden yeni kanıt dosyalarıyla
doğrulandı. `/usr/local/bin` ve msgq yazılabilir; systemd sorgusu başarılı.
Hazır paket yedeklenerek iki CLI kopyasına, iki agentbook'a ve bekçinin iki dosyasına
uygulandı. Yalnız `blueprint.service` yenilendi. Pane PID karşılaştırmasında değişiklik
yok; agentlar veya ortak Codex daemon'u yeniden başlatılmadı. Blueprint'in aynı thread,
astra/high ve noSandbox launch kaydı korundu.

Hostta ortaya çıkan iki fark da düzeltildi:

- Remote TUI başlangıç argv'sindeki eski thread, yalnız açık operator pini ve
  app-server'ın eski thread için `notLoaded` doğrulamasıyla aşılır. İki açık thread
  veya doğrulanamayan eski thread belirsiz kalır.
- Remote konuşmanın başlangıç cwd'si sonradan değişebilir. Güncel cwd ve transkript
  yolu app-server'dan doğrulanıp dosyanın UUID'si kontrol edilir; en yeni dosya seçilmez.

## Canlı kanıt

- CLI producer ve çalışan daemon executable SHA'ları eşit;
  `daemon_verification=verified executable`.
- op-main compact sonrası **12222** token okundu; eski 361381 sayısı gösterilmiyor.
- Studio'nun doğru remote thread'i, modeli/effort'u ve tokenı okunuyor.
- Bekçi `--kuru` geçti. Eski toplam/mmtime'dan iş alarmı üretmiyor; kapsam dışı ve
  belirsiz ölçümleri açıkça raporluyor.
- `bp msg server-main` test kodu **BP-HOST-20260905-1527**, gerçek alıcı thread
  `01a0715e-a5c0-7171-adf3-c95343ce6d5b` içinde satır 579'da,
  **15:27:05.508 UTC** inbound user mesajı olarak bulundu. CLI sonucu da delivered.

Unconfined yürütmede per-execution kimlik kanıtı yoksa, açık thread kaydıyla
eşleşen zarf okunabilir bir ipucu taşır: `[blueprint?]`. Soru işareti doğrulanmamış
çağıranı belirtir; UUID `bp whoami` tanısında korunur. Eşleşmeyen thread hâlâ
`codex?:UUID` olur. Bu isim ipucu root/force veya hiyerarşi yetkisi sağlamaz;
AGENT/cwd/eski TMUX'tan isim çıkarılmaz. Yeni/pinsiz threadlerin eşlemesi ve
yetkili servis kimliği ayrı işlerdir.

Okunabilir zarf canlıda da doğrulandı: **BP-NAME-20260905-1600**, normal `bp msg`
ile root'un aynı thread'ine **16:00:37.624 UTC**, satır 683'te `[blueprint?]`
olarak ulaştı. Kanıt `readable-sender-proof.json`; ilgili identity/book/bp/wa
race testleri geçti. Bu yayında da pane süreçleri değişmedi.

Kanıtlar `/srv/blueprint/dist/rollout-20260905-host/` altında:
`host-apply.log`, `message-send.log`, `final-status.json`,
`final-public-verification.json`. Güncel binary hash'leri ayrıca
`dist/laptop-release/checksums.txt` içinde. İlk host paketi ve sonraki çubuk
yayınlarının yedekleri korunur; eski raporlar tarihsel kayıttır.

## Tuna'nın son çubuk tercihi

Çalışma tek dönen sembol, idle `·`, unknown `?`, blocked `!`, dead `×`.
Mevcut tmux yenilemesi kullanılır; scroll veya refresh ayarı değiştirilmez.
Model kısa kalır: `astra high`. Kota **kullanım yüzdesidir**: `gpt 13%`,
`cc 19%/13%`. Claude'da sıra 5 saatlik/haftalık, Codex'te haftalık/kısa dönemdir; tek veri varsa yalnız
o gösterilir. `quota`, `left`, uzun sağlayıcı ve durum etiketleri kaldırıldı.

## Cache süresi düzeltmesi

Sabit 1 saat varsayımı kaldırıldı. [TTL incelemesi](cache-lifetime.md) ve
`cache-ttl-proof.json`: probot-egitim'in kaydı 1h TTL ve `warm~` gösterirken
Codex'in TTL'siz ölçümü `age` gösteriyor. Cache/book/bp/daemon race testleri ve
vet geçti; 16:29:53 UTC yayını yalnız Blueprint servisini yeniledi. Pane PID'leri
değişmedi; CLI/daemon ve dört public platform hash'leri doğrulandı.

16:59 UTC [Claude eşleme yayını](claude-binding-2026-09-05.md) bunların üzerine
uygulandı. Prepress kaydı düzeltildi; Studio'nun kilitli mesajı gerçek inbound
olarak doğrulandı. Probot-main'in eski süreç kaydı ve dolu composer engeli sürüyor.

18:06 UTC: [YAML yapılandırması](configuration.md) ve yorumlu laptop config kurulumu
host/public paketlerine yayımlandı. Sunucunun JSON config'i ve pane PID'leri aynı
kaldı. Config/bp/ntfy race testleri, vet ve sekiz gerçek-tmux laptop testi geçti.
Public installer temiz, ayrılmış bir HOME altında indirildi ve çalıştırıldı;
oluşturulan YAML `bp config check` ile doğrulandı. Kanıt `yaml-install-proof.json`.

18:24 UTC: laptopun yeşil varsayılan tmux çubuğu düzeltildi. Yerel CLI açılışı
artık yalnız kendi oturumuna bp name/bar komutlarını kuruyor; `bp setup` aynı
BP_HOME altındaki açık yerel bp oturumlarını agentları kapatmadan onarıyor.
Çubuk işleri kurulu binary'nin tam yolunu ve ilgili ortamı kullanıyor. Global
tmux tuş/fare/scroll ayarları korunuyor. Dokuz gerçek-tmux testi geçti; yeni test
eski çubuğun onarımını, pane PID'sinin ve diğer oturum/global ayarların korunmasını
kontrol ediyor. Hedefli Go testleri ve vet başarılı. Dört hedef derlendi; gerçek
macOS terminali bu ortamda test edilmedi.

Host CLI/daemon ve altı public dosya SHA-256 ile doğrulandı; host pane PID'leri
değişmedi. Binary: `e8e7283b9096ae192028a3e1f1d578edb0b4865b5551c8afe60e0c3474f7f117`.
Yedek: `dist/local-bar-backup-20260905T182441Z`; test kaydı
`/tmp/bp-local-bar-integration-v2.log`. Laptop–sunucu debug/ağ kurulumu Tuna'nın
son kararıyla ertelendi; bu yayında bağlantı/port/peer eklenmedi.

19:43 UTC: alias koruma düzeltmesi host/public paketlerine yayımlandı. Setup artık
`unalias` kullanmıyor; `claude='claude --dangerously-skip-permissions'` gibi aliaslar
korunarak bp fonksiyonuna seçenekleriyle giriyor. Özel shell fonksiyonları
korunuyor. Gerçek Bash/Zsh ve tmux ile 11 test, hedefli Go testleri ve vet geçti.
Public installer ayrı HOME altında indirilip çalıştırıldı; alias satırı korundu.
Kanıt: `alias-install-proof.json`; yedek `dist/alias-backup-20260905T194301Z`.

19:47 UTC: Claude assistant kayıtlarının üst düzey `effort` alanı ortak runtime
ölçümüne eklendi. Cache/book/bp race testleri ve vet geçti. Model değişimi,
sidechain/synthetic kayıtlar, eksik veya farklı şekilli effort ve çubuğun global
settings yerine transcripti kullanması regresyon testleriyle doğrulandı.
Host CLI/daemon hash eşleşmesi ve altı public dosya indirerek doğrulandı.
Canlı probot-egitim/probot-studio çubuğu `fable med`, kitap-main `opus high`;
`status --json` aynı effort değerini taşıyor. Kanıt `claude-effort-proof.json`;
yedek `dist/claude-effort-backup-20260905T194744Z`. Agent pane PID'leri değişmedi.

20:31 UTC: yerel açılışın otomatik konuşma/ölçüm eşlemesi yayımlandı. Yeni
`localObservation` varsayılan açık; küçük kayıtlar `stateDir/local/` altında.
Claude açılışa özel SessionStart/status-line gözlemi; Codex kernel writer-lock
kanıtı kullanıyor. Codex'e hook/trust istisnası eklenmedi. Konuşma kopyalanmadı,
global CLI ayarı veya agent model/effort'u değiştirilmedi. Laptop çubuğu kalan
contexti `bar.context: remaining` ile gösterir; kaynak yoksa sayı uydurmaz.

On iki gerçek-tmux/sahte-CLI testi geçti; eski elle agentbook/thread hazırlığı
kaldırıldı. Aynı cwd'de iki farklı CLI/thread, ilk açılış model/context verisi,
Bash/Zsh aliaslar, kuyruk/busy/Vim korumaları ve native kayıttan doğrulanmış mesaj
teslimi kapsandı. Race testleri ve vet geçti. macOS parser testi ve iki macOS
cross-build geçti; gerçek macOS/laptop kullanım testi bu sunucuda yapılmadı.
Çalışan gerçek Codex 0.153.4'te salt-okunur kernel-lock eşlemesi, doğru thread,
model/effort ve context ölçümüyle ayrıca doğrulandı; CLI'a input gönderilmedi.

Host CLI/daemon hash eşleşti, altı public dosya indirilerek doğrulandı. Public
installer ayrı temiz HOME'da aliası korudu, agentbook ve gözlem/kalan-context
varsayılanlı geçerli YAML oluşturdu. Kanıtlar `local-observation-live-proof.json`
ve `local-observation-install-proof.json`; yedek
`dist/local-observation-backup-20260905T203128Z`. Host pane PID'leri aynı kaldı.
Eski açık laptop oturumlarına başlangıç ayarı enjekte edilmez; konuşma resume
picker ile seçilip tekrar açıldığında yeni eşleme çalışır.

## 2026-09-08 — laptop mouse ve harness bağımsız renk

Host CLI, Blueprint daemon ve dört public platform paketi güncellendi. SHA-256
`d856b188e3582993d576e3d6df330012d988e596696a00c9b14e31042de0aa0d` (Linux amd64).
Kanıt: `dist/mouse-color-20260908/rollout.json`, `public-verification.json`.
Yedekler aynı dizinde `backup-20260908T103839Z`; tüm pane PID'leri korundu.
Ortak Codex veya diğer agentlar yeniden başlatılmadı.

Yerel mouse varsayılan açık, YAML `localMouse: false` ile kapatılabilir. Gerçek
özel tmux/PTTY testinde wheel→copy-mode→selection doğrulandı; global mouse/prefix
ve diğer oturumlar korundu. 12 kota kullanmayan yerel CLI testi, tüm Go paket
testleri geçti; son renk seçimi ekinden sonra bp/book/config/tmux testleri ve vet
tekrar geçti. `bp color <agent> [--json|auto|color]` eklendi.

Paket önceki Claude trust-modal kaynak düzeltmesini de içerir; onun kanıtı
regression fixture testidir, yeni bir gerçek Claude açılışı yapılmadı. Gerçek
laptop/terminal clipboard davranışı bu sunucudan doğrulanmadı.

## 2026-09-08 — bar varsayılanları ve compact teslimi

Son paket: `dist/compact-delivery-20260908/rollout.json` ve
`public-verification.json`. Host CLI/daemon SHA-256:
`86a942f06c9364510c018361f936911651d70d89e0422aed53110ab45fae6bfe`.
Dört platform paketi ve public checksum/installer doğrulandı. Pane PID'leri korundu.

Sunucu/laptop bar.context varsayılanı used; açık YAML remaining seçimi korunur.
bar.defaultColor eklendi; agenta özel renk daha öncelikli. Bilinmeyen cache TTL
hâlâ age gösterir. Bar renk/config ve precedence testleri geçti.

Manuel compact tamamlanmasını native boundary+summary+local stdout dizisiyle
tanıma eklendi. Bayat open turn senaryosu özel tmux ve fake Claude ile üretildi;
ek kullanıcı girdisi olmadan tek mesaj ve transcript-confirmed teslim doğrulandı.
13 yerel entegrasyon testi geçti; q --retry eki sonrası ilgili test tekrar geçti.
book/bp/daemon/msgq testleri ve ilgili race/vet geçti. Laptop olayının kendi
transcriptine erişilmedi; gerçek ağ/laptop teslim kanıtı olduğu iddia edilmiyor.
`bp q --retry` mevcut kuyruğu güncel CLI ile normal korumalar altında işler;
eski local worker için agentı yeniden başlatmaya alternatif.

## 2026-09-08 — kitap-prepress writer binding

Host CLI/daemon ve public dört platform güncellendi. Kanıtlar
`dist/writer-binding-20260908/{rollout,public-verification,installed-status}.json`.
Linux amd64 SHA-256 `fe9040e60d87ed8bf228fa6eceef1dadc26cad12cacd8d3774ddf360cf45d8e0`.
Pane PID'leri korundu; hiçbir agent yeniden açılmadı veya mesajla bölünmedi.
Canlı kitap-prepress kernel writer thread'i 01a08082-4411-73a0-9a02-160b72ce9c95
ile eşleşti. Gerçek hedef çalışırken mesaj teslimi denenmedi. Pinsiz fake Codex
ile özel tmux testinde tek ve transcript-confirmed teslim geçti. book/tmux/bp/
daemon/msgq testleri, book vet geçti. Remote ve explicit pin akışları korunur.

## 2026-09-08 — native ad/renk/model gözlemi

Son paket `dist/native-metadata-20260908/`: rollout.json, public-verification.json,
installed-status.json. Host CLI/daemon/public Linux amd64 SHA-256:
`af0c06576ca733ed00626b56693bd21211c2b84ef8ddf8bb8d9f48d297b7bc3c`.
Pane PID'leri korundu; agent/model/effort yeniden başlatılmadı/değiştirilmedi.

Claude native /rename başlığı incremental ve thread-bound display metadata olarak
izlenir; bp name/status ve unique msg/peek/color alias çözümü eklendi. Ad
çakışması ve reserved/canonical identity taklidi engellenir. Native renk plakasında
yeni ad da tanınır. Stable session/queue/parent/authority alanları korunur.

Güçlü canlı süreç kanıtı varsa embedded Codex writer ve Claude süreç session
eşleşmesi eski launch pinlerinden öncelikli telemetry sağlar. Native title değişimi
Claude process-bound transcript erişimini kesmez. Authority pinleri güncellenmez.

15 kota kullanmayan yerel CLI/tmux testi, tüm Go paket testleri ve ilgili
race/vet geçti. Gerçek laptopta native CLI denemesi yapılmadı; özel tmux/fake
harness ile /rename orch→renk→alias→tek transcript-confirmed teslim doğrulandı.
Devtool review ayrıca docs/devtool-review-2026-09-08.md içinde; version/update/
doctor/release önerileri uygulanmış sayılmaz.
