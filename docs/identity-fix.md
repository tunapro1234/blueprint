# 5 Eylül 2026: yanlış server-main kimliği

Yeni canlı tekrar: [q343864794 kanıtı](identity-incident-q343864794.md).
Yanlış zarfın gerçek Studio ana thread'indeki çıplak bp çağrısından çıktığı
doğrulandı. Sonraki [host kurulumu](live-rollout-2026-09-05.md) tamamlandı;
aşağıdaki olay ve ilk doğrulama kayıtları tarihsel bağlamdır.

Kanıtlı kayıt: `q663395417`, gerçek çağıran Luna thread'i
`01a07133-19f4-7370-a4b5-05b2b878d824`, üst Astra thread'i
`01a0712d-46d1-7223-b666-e44fb229c782`. Mesaj `server-main` adına teslim edilmiş.
Olay raporu: `/srv/probot/egitim/astra/bp-kimlik-hatasi-2026-09-05.md`.

Kök neden, ortak app-server'ın başlangıç `TMUX`/`TMUX_PANE` ortamının başka
thread'lerin araç çağrılarına taşınması. Eski `Resolve`, hedefsiz `#S` yanıtını
kesin kimlik sayıyordu. Olaydaki yol bu; ayrıca bulunan `server-main` fallback'i
ve yalnız etikete bakan hiyerarşi kontrolleri riski büyütüyordu.

## Kaynak düzeltmesi

- Codex için ortamın thread ID'si tek başına yetmez. Linux per-execution sandbox
  yardımcısının başlangıç bağlamıyla çapraz kontrol edilir. Ortak app-server ve
  exec-server başlangıç ortamı bu kanıtı sağlamaz.
- Agentbook'un **explicit `identityThreadId`** kaydı ve doğru UUID'li session_meta
  birlikte gerekir. Cwd, son rollout, AGENT, BP_SESSION veya otomatik launch
  keşfi kimlik kaydı yerine kullanılmaz. Launch/thread çelişkisi reddedilir.
- Subagent parent bağlantısı Codex session_meta'dan okunur. Zarf
  `<parent>/subagent:<UUID>` olur; parentın adıyla veya yetkisiyle göndermez.
- Normal tmux çağrısında hedef açıkça `%pane` seçilir; pane PID'sinin çağıranın
  süreç zincirinde olduğu doğrulanır. Eksik/ölü/eski pane ve ortak sunucu atası
  reddedilir. Yalnız `-t TMUX_PANE` eklemekle yetinilmez.
- Eşleşmeyen thread `codex?:<UUID>`, kimliği olmayan çağıran `bilinmiyor`, AGENT
  bildirimi `agent?:<ad>` görünür. `server-main` fallback'i kaldırıldı.
- Açık thread kaydı eşleşse bile çağıran doğrulanamıyorsa okunabilir `<ad>?`
  ipucu gösterilir; UUID tanıda kalır, kesinlik ve yetki kazanılmaz.
- Slash, announce, compact ve force kontrolleri doğrulanmış ana agent ister.
  Bir subagentın doğru atfedilmesi, parentının yetkilerini alması anlamına gelmez.
- `bp whoami` salt okunur JSON kimlik tanısı sağlar. Alternatif AGENTBOOK dosyası,
  kurulumun yapılandırılmış kitaplarından değilse hiyerarşi yetkisi sağlayamaz.

## Doğrulama sınırı ve davranış etkisi

Linux sandbox yardımcısı olmayan bir Codex yürütme bağlamını bu sürüm henüz
kesin doğrulayamaz. Özellikle bazı unconfined/shared-server ve macOS yollarında
mesaj belirsiz etiketle gider, hiyerarşi/force komutu reddedilir. Bu durumda
AGENT, cwd veya daemon'ın eski tmux bilgisinden sessiz bir fallback yapılmaz.
Yeni local Codex oturumları da açık thread kaydı olmadan kesin isim alamaz;
hedef isimleriyle normal mesaj gönderme/alma kullanılabilir.

WhatsApp bridge'in yalnız `AGENT=whatsapp` kullanarak yaptığı force çağrıları artık
reddedilir. Bridge'e doğrulanabilir servis kimliği bağlanmadan force işlevinin
korunduğu söylenemez. Bridge değiştirilmedi; yayın öncesi bu etki incelenmeli.

Bu düzeltme **aynı UID/root altında kötü niyetli süreçlere karşı mutlak kimlik
izolasyonu sağlamaz**. Aynı kullanıcı tmux/book dosyalarını ya da kendi çalışma
ortamını değiştirebilir. Böyle bir güvenlik garantisi için ayrı OS yetkileri ve
kimliği doğrulayan aracı gerekir; Tuna'nın [karar listesinde](tuna-pending.md).
Yama, kanıtlanan yanlış miras ve belirsizlikten yetki üretme yollarını kapatır.

## Vim / terminal üzerinden zarf değiştirme

Bracketed paste tek başına yeterli değildi: gövdedeki gerçek `ESC [201~` paste'i
erken bitirebilir; arkasındaki Ctrl-U, Vim komutları veya Enter editöre tuş olarak
gidebilir. Kaynak düzeltmesi mesaj/announce girişinde, spool/queue yazımında ve
tmux gönderim/onarım yollarında ortak doğrulama kullanır. Gerçek ESC, CR,
Backspace, diğer C0/C1 kontrolleri, DEL, geçersiz UTF-8 ve Unicode yön kontrol
karakterleri reddedilir. LF/TAB, Türkçe ve normal Unicode metin korunur.
Kontrol dizisi anlatmak için gerçek kontrol baytı yerine yazılı `\x1b` gibi
gösterimler kullanılabilir. Hata mesajı saldırı baytlarını terminale geri basmaz.

Gönderen alanında köşeli parantez ve satır/alan ayırıcıları reddedilir. Eski
güvensiz kuyruk kayıtları silinmez veya teslim edilmiş sayılmaz; neden belirtilerek
bekletilir, Enter/cleanup ve sahiplik kanıtı olarak kullanılamaz. Eski güvensiz
spool kaydı yüklenirken açık hata döner; dosya korunur. Federasyonun mevcut
kontrol karakteri temizlemesi sürer; son yerel kuyruk ve terminal kontrolleri
ondan bağımsızdır. CRLF içeren yerel mesajlar LF ile yeniden gönderilmelidir.

Bu, mesaj gövdesindeki düz `[server-main]` iddiasını doğrulamaz. Gövde alıntısı,
rol taklidi veya doğal dille prompt injection hâlâ içerik düzeyinde risktir;
yetki kararı gövdedeki etiketten çıkmamalıdır. Ayrı doğrulanmış metadata/broker
ve OS yetki sınırı konusu Tuna'nın karar listesinde kalır.

Regresyonlar Claude/Codex Vim Normal/Insert için escape ile paste'i bitirme,
etiketi silip yazma, erken submit, görsel yön değiştirme, force ve legacy
recovery yollarını kapsar. Özel gerçek tmux soketindeki sahte CLI testinde
saldırı reddedildikten sonra normal çok satırlı mesaj tek kez teslim edilir.

## Kota harcamayan regresyonlar

Aynı daemon/TMUX ile farklı ana thread'ler; CLI-içi Luna; eksik/çelişen/çoğul
thread kayıtları; döngülü parent; yanlış UUID; yalnız komut ortamını değiştirerek
root thread'i iddia etme; eski/ölü/yok pane; AGENT=server-main/whatsapp iddiası;
mesajın gerçek spool zarfı, slash/announce/compact/force reddi birlikte sınanır.
Vim ve laptop testleri özel tmux soketi ve sahte CLI'lar kullanır. Model çağrısı,
canlı mesaj, filo taşıması veya daemon restart bu doğrulamaya dahil değildir.

## Yayın için somut kayıtlar

`dist/codex-books/` incelenecek kopyaları içerir. Kimlik pinleri yalnız Tuna/server-main
bağlamında açıkça verilen üç eşleşmeden gelir; diğer agentlar tahminle doldurulmaz.
Üretim dosyaları ve canlı binary değiştirilmedi.

```sh
python3 scripts/migrate-codex-book.py \
  --thread 01a0617e-29f9-79a3-be66-72ea1dec4718 \
  --bind blueprint=01a070c4-3f56-7213-b8ad-19fa7d6f5e4d \
  --bind probot-egitim-astra=01a0712d-46d1-7223-b666-e44fb229c782
```

Bu varsayılan komut yalnız inceleme kopyası yazar. Pin eklemek Astra/blueprint'in
launch, sandbox, model veya effort ayarlarını değiştirmez. Gerçek uygulama için
[geçiş notundaki](../CODEX-CUTOVER.md) backup/servis sırası ve güncel aday checksum'u
incelenmeli; ortak Codex daemon'u topluca restart edilmemeli.
