# Kitap raporu: Claude oturum eşleme düzeltmesi

Kaynak rapor: `/srv/kitap/.orchestration/BP-BUG-RAPORU-2026-09-05.md`.
HEAD tek başına canlı sürümü tanımlamıyordu: bp değişiklikleri çalışma ağacındaydı.
Canlı CLI/daemon SHA karşılaştırması `dist/rollout-20260905-host/final-status.json`
ile yapılır.

## Doğrulanan ve düzeltilenler

- Prepress'in gözlenen runtime'ı Claude iken eski Codex `launch.resumeId` filtresi
  tek Claude transkriptini eliyordu. Launch pinleri artık sağlayıcıya göre ayrılır.
- Claude'un pane altındaki ana süreci, `~/.claude/sessions/<pid>.json` içindeki
  sessionId ile eşlenir. PID, `/proc` başlangıç sayacı, gerçek cwd, kayıt cwd'si,
  UUID ve interactive türü doğrulanır. Ana sürecin altındaki araç/subagentlara
  inilmez. Ortam değişkeni, en yeni mtime veya eski başlangıç argv'si kullanılmaz.
- Probot-studio'nun çakışan başlıkları bu kayıtla ayrıldı. Prepress doğru
  `69632f85-5244-4ec1-866c-31aa4adef0be` konuşmasına bağlandı.
- Eksik pin, eksik klasör ve birden çok eşleşme ayrı hatalardır; dizin ve çakışan
  yollar hata metninde görünür. `bp status --json` ayrıca her agent'ın gerçek
  `agentbook_paths` listesini verir. Belirsiz eşleşme force ile aşılamaz.
- `--claude` / `--codex` geçişinde diğer sağlayıcının resume/remote/izolasyon
  seçenekleri taşınmaz. Açıkça istenen resume bulunamazsa yeni konuşma açılmaz.
  Başarılı Claude açılışı launch kaydını günceller; çözülen UUID korunur.

## Kalan gerçek engeller

Probot-main'in süreç kaydı ve başlangıç argv'si aynı eski UUID'yi gösteriyor:
`4bd47c4f-e853-4eff-bdd0-f3b8444531f8`. PID başlangıcı doğrulansa da registry
son güncellemesi ve transkriptin son açık iş kanıtı **17 Ağustos**. Dolayısıyla
PID eşleşmesi güncel aktiviteyi kanıtlamıyor; sonuç `unknown / turn evidence stale`.
Diğer dosya `f9e902f0-eeed-4fed-a810-973b628ee8a7` daha yeni diye seçilmedi.
Composer'da mevcut metin var; girilmedi, silinmedi, Enter gönderilmedi. Sekiz
mesajın tamamının teslim edildiği iddia edilemez. Agent sahibi kullanıcı girdisini
kontrol edip gerçek aktif konuşmayı teyit etmeli; gerekirse uygun bir boşlukta
eski Claude istemcisini güncellemelidir. Bu çalışmada agent restartı yapılmaz.

D olayının eski q885097408 kaydı bulunamadı; mükerrer gönderim yeniden
kanıtlanmadı. Modal/input korumaları ve mevcut mükerrerlik regresyonları çalıştırılır;
bu, tarihsel olayın yeniden üretildiği anlamına gelmez.

## Agentbook sahipliği

Kitap filosu bp için şu anda `/srv/server-main/agentbook.json` içindedir.
`/srv/kitap/.orchestration/agentbook.json` config listesinde yoktur ve bp için
bağlayıcı değildir. İki defteri birleştirip yeni pin/hiyerarşi çakışması yaratacak
sessiz bir config değişikliği yapılmadı; kitap-main'e bu ayrım bildirilecek.

Yeni mosh/uyandırma ve YAML özellikleri yalnız `tuna-pending.md` taslağındadır.

## Canlı yayın ve teslim

16:59 UTC: CLI/daemon ve dört public binary güncellendi; SHA ve indirilen
artifact'lar doğrulandı. Yalnız blueprint.service yenilendi, pane PID'leri aynı.
Prepress'in ana defterindeki launch, mevcut doğrulanmış Claude UUID'siyle
düzeltildi; yedek `dist/claude-binding-backup-20260905T165914Z/` altında.

Probot-studio'nun q814566394 mesajı gerçekten teslim edildi: eşleşen transkriptte
satır **28288**, **16:59:20.766 UTC**, inbound UUID
`11267304-a853-4303-ab5b-b9c3efde3b50`. Kuyruk gövdesi birebir eşleşti;
kanıt `dist/rollout-20260905-host/claude-binding-delivery-proof.json`.
Kitap-main'e normal bp mesajıyla sonuç ve defter sahipliği bildirildi, delivered.
Go race (tmux/book/bp/msgq/daemon) ve vet geçti.
