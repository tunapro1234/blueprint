# Terminal / tmux / pano taktikleri (ada, 2026-07-24)

Bu doküman filodaki tmux+Claude Code+kopyalama/yapıştırma kurulumunun NEDEN böyle olduğunu anlatır.
Ayarları değiştirmeden önce oku. Sahibi: ada (server-main). İlgili bp backlog: P1-P4 (bp model/effort,
RC sweep, adopt, --session) — bkz ada'nın bp-tool-ownership talimatı.

## 1. Kurulumun özeti

Kullanıcı (Tuna) laptop'tan (Arch + kitty) SSH ile bağlanır, agent'lar tmux oturumlarında Claude
Code TUI çalıştırır. İstenen davranış:

- Metin SEÇMEK panoyu ASLA ezmez (Claude Code içinde de, shell'de de).
- Ctrl+C = seçileni laptop panosuna kopyala (Claude Code içinde bile).
- Ctrl+C seçim yokken normal interrupt/iptal olarak çalışmaya devam eder.

## 2. Neden bu kadar uğraştık — mekanizma

Claude Code TUI, tmux içinde çalıştığını görünce fare seçimini KENDİ yakalar ve seçim biter bitmez
metni `tmux set-buffer`'a yazar ("copied N chars to tmux buffer" toast'u). Kapatma ayarı yok
(upstream feature request: anthropics/claude-code #60755). İki sızıntı kanalı vardı:

1. tmux `set-clipboard on` → buffer değişimini OSC52 ile dış terminale (kitty) aynalıyordu.
2. tmux `allow-passthrough on` → Claude Code OSC52'yi passthrough ile tmux'u BYPASS edip
   doğrudan kitty'ye basabiliyordu. (set-clipboard off yetmedi, sebep buydu.)

kitty tarafı masum: copy_on_select default kapalı. Lokal Claude Code'da sorun olmamasının sebebi
tmux'un yokluğu (buffer yolu hiç kurulmuyor).

## 3. Güncel çözüm (üç parça, ~/.tmux.conf + 2 script)

- `set -s set-clipboard external` + `set -g allow-passthrough off`
  → uygulamaların panoya ulaşabildiği HER tty kanalı kapalı. Seçim sadece tmux buffer'ına düşer.
- `/usr/local/bin/tmux-osc52`: stdin'i base64'leyip OSC52'yi `#{client_tty}`'ye DOĞRUDAN yazar.
  tmux'un set-clipboard/passthrough ayarlarından bağımsız — bilinçli kopyanın tek çıkış kapısı.
  (external modda tmux'un kendi push'u kitty'ye işlemedi; bu script o belirsizliği de bypass eder.)
- `/usr/local/bin/tmux-smart-cc` + `bind -T root C-c run-shell 'tmux-smart-cc "#{pane_id}"'`:
  Ctrl+C her pane'de önce script'e gelir. Kural: son 90 sn içinde OLUŞMUŞ ve daha önce
  KOPYALANMAMIŞ buffer varsa (state: /tmp/.tmux-cc-last-buffer) → `save-buffer - | tmux-osc52` +
  "panoya kopyalandı ✓"; yoksa → `send-keys -t <pane> C-c` (interrupt aynen geçer).
  Bilinçli takas: seçimden sonraki İLK C-c her zaman kopyadır; iptal istiyorsan ikinci kez bas.

Ek binding'ler:
- copy-mode-vi: `MouseDragEnd1Pane` UNBIND (bırakınca otomatik kopya yok); `C-c`/`y` →
  `copy-pipe-and-cancel "tmux-osc52"`. Çift/üç tık kelime/satır SEÇER ama kopyalamaz.
- `Ctrl-a y`: son buffer'ı panoya bas (smart-cc'nin prefix'li yedeği).

## 4. Test prosedürü (değişiklik sonrası koş)

1. Claude Code'da seç, Ctrl+C BASMA → laptopta Ctrl+V eski içeriği vermeli.
2. Seç + Ctrl+C → "panoya kopyalandı ✓" + laptopta yeni metin.
3. Seçimsiz Ctrl+C → Claude Code'da iptal / shell'de interrupt.
4. `echo -n test123 | tmux-osc52` → laptop panosuna test123 düşmeli (OSC52 hattı sağlam mı).

## 5. Görsel yapıştırma (bp img) — ayrı sistem, karıştırma

- Laptop→server görsel: `bp img` LAPTOPTA koşar (bp connect config'iyle): lokal panodaki PNG'yi
  (wl-paste/xclip/pngpaste) SSH üzerinden `bp img recv`'e akıtır; server
  `/srv/server-main/clipboard/clip-YYYYMMDD-HHMMSS.png` yazar ve yolu basar — o yol Claude Code'a
  verilir. Kaynak: cmd/bp/img.go (imageDropDir sabiti).
- Metin panosu (OSC52) ile İLGİSİZ; OSC52 görsel taşıyamaz.

## 6. Diğer tmux kalıpları (filo yönetimi)

- Prefix `Ctrl-a`. Scroll/copy-mode: `Ctrl-a [` veya tekerlek; mouse on, history 100k, vi keys.
- Meşgul kontrol: pane'de "esc to interrupt" = çalışıyor → MESAJ GÖNDERME (bp deliver katmanı
  kuyruğa alır; elle send-keys yapıyorsan önce bak).
- RC (remote control) resume sonrası "Enter to select" menüsü açık kalır → 2-ardışık-temiz-tur
  Enter süpürmesi (bp remote'ta var; bp open'a gömülmesi backlog P2).
- `/model` ve `/effort` GLOBAL ~/.claude/settings.json'u da yazar → agent'larda kalıcı model için
  cwd'ye `.claude/settings.local.json` pin'i; orchestrator'lar pinlenmez (Tuna elle yönetir).
- Aynı klasörde iki agent = resume/memory/settings çakışması → agent başına ayrı cwd ZORUNLU.

## 7. Süreç kimliği: pgrep tuzakları (2026-07-25, iki gerçek olay)

Aynı gün iki agent `pgrep` çıktısına güvenip yanlış süreci öldürdü/öldürecekti:

1. **Başkasının süreci sanma:** codex oturumlarının komut satırları birebir aynı görünür
   (`codex --search -c model_reasoning_effort=high`). compec-site kendi çağrısı zannedip
   server-crash'in nöbetçi oturumunu öldürdü.
2. **Arayanı eşleme:** `pgrep -f "node /srv/whatsapp/bridge.js"` arayan shell'in kendisini de
   döndürür (desen kendi cmdline'ında geçer) → "duplicate instance var" yanılgısı.

**Kurallar:**
- Arka planda süreç başlatırken PID'i o anda kaydet, sadece onu öldür; stdin'i kapat
  (`... < /dev/null`) ki beklemede kalmasın.
- Sonucu pgrep eşleşmesiyle değil **görev çıktı dosyasından** doğrula.
- Arama yaparken köşe-parantez numarası: `pgrep -f '[c]odex --search'`; ya da her PID için
  `ps -o pid,ppid,lstart` ile sahipliği doğrula.
- Başkasının süreci ise ÖLDÜRME — sahibine `bp msg` at.
- **Boşta duran süreci "zararsız" sayma.** Sürecin ne YAPTIĞINA bak, meşgul olup olmadığına
  değil: 12 saattir boşta duran bir süreç pekâlâ canlı bir nöbetçi olabilir (2026-07-25 olayının
  asıl dersi; izleme 4s09dk kör kaldı).

**Codex özel notu:** yeni codex sürümü çıktığında codex açılışta `npm install -g @openai/codex`
dener; bwrap sandbox'ında `/root/.npm` salt-okunur olduğu için EROFS alıp kapanır ve tmux'ta boş
zsh kalır. Çözüm: güncellemeyi sandbox DIŞINDA çalıştır (ada yapar), sonra agent'ı yeniden aç.

## 8. "Bitti" raporu ≠ iş bitti (2026-07-25, compec-main bulgusu)

Bir subagent Drive indirme işini arka planda başlattı, kendi açısından teslim tamam sayıp
"bitti" raporu verdi ve kapandı. Kapanınca **başlattığı arka plan süreci de öldü**: zincir
805/1138 dosyada kesilmişti, kimse fark etmedi.

**İkinci yarısı (aynı gün, compec-main):** süreç AYAKTA olduğu hâlde İŞ ÜRETMEYEBİLİR — Drive
kotası dolunca indirici çalışmaya devam etti ama sayaç 805→806'da takıldı, log baştan sona
"Quota exceeded". Yani "süreç var mı?" ile "ilerliyor mu?" ayrı sorulardır; izleme, çıktının
ARTTIĞINI görmeli. (Bu ders health-watch'a da uygulandı: nöbetçi hem koşuyor mu hem alarm
sonrası incidents.log'u güncelliyor mu diye kontrol ediliyor.)

**Kural:** tamamlanmayı agent'ın BEYANIYLA değil, **ölçülebilir kanıtla** doğrula —
dosya sayısı, satır sayısı, hedef boyutu, HTTP kodu, DB kaydı. Rapor "yaptım" diyorsa
"kaç tane?" diye sor ve say.

**Uygulama:**
- Uzun/bulk işi başlatan agent, iş bitene kadar AÇIK kalmalı; kapanacaksa işi systemd
  servisine ya da `run_in_background` + harness takibine devret (sahipsiz arka plan süreci bırakma).
- İndiriciler/işleyiciler **yeniden başlatılabilir** olsun: hedefte varsa boyut/hash kontrolüyle
  atlasın, baştan başlamasın.
- Fan-out sonrası kabul kriteri şart: "N dosyanın N'i" gibi sayılabilir bir eşik.

## 9. Agent kapanınca bıraktığı DURUM sahipsiz kalır (2026-07-25, alp'in yakalaması)

probot-pil kapatıldığında, incelemesi için shop'a koyduğu koruyucu SKU hold'u yerinde kalmıştı —
kimsenin sahiplenmediği, kaldırılmayı bekleyen bir kısıt. alp fark edip kaldırdı ve bulguyu
JSONL'e arşivledi.

**Kural:** bir agent kapatılırken sadece süreçleri değil, **dış dünyada bıraktığı durumu** da
devret veya temizle: hold/lock/rezervasyon, systemd servisi, cron kaydı, açık PR/branch,
nginx location, geçici DNS kaydı, paylaşılan dosya kilidi.

**Kapatma kontrol listesi:** (1) çalışan/arka plan işi var mı → devret ya da bitir,
(2) dışarıda kısıt/kayıt bıraktı mı → kaldır ya da sahibi belirle, (3) bulguları kalıcı yere yaz
(JSONL/log/doküman) ki resume gerekmeden erişilebilsin, (4) agentbook'ta status:closed.

Aynı aile: Bölüm 7 (süreç sahipliği), Bölüm 8 ("bitti" ≠ bitti, canlılık ≠ ilerleme).
