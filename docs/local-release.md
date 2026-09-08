# Laptop/Vim/kimlik adayı — 2026-09-05

**Güncelleme:** public ve host kurulumu tamamlandı; [canlı kanıt](live-rollout-2026-09-05.md).
Aşağıdaki host engeli notları eski aşamayı anlatır.

Kaynak implementasyonu ve paketler hazır; **public kurulum URL'si 5 Eylül'de
güncellendi, host CLI/daemon henüz güncellenmedi**. Public dosyalar normal URL'lerinden
indirilip SHA-256 doğrulandı. [Host paketi ve kanıt](host-rollout-2026-09-05.md).
Paketler `dist/laptop-release/` altında:

- `bp-linux-amd64`, `bp-linux-arm64`
- `bp-darwin-amd64`, `bp-darwin-arm64`
- `install.sh`, `checksums.txt`

Bu aday önceki Codex status/remote/mesaj düzeltmelerini, laptop run/setup akışını,
Claude/Codex Vim korumalarını, 5 Eylül kimlik düzeltmesini ve
[ortak runtime/kota/bekçi sözleşmesini](runtime-status.md) birlikte içerir.
`dist/bp-codex` önceki aşamanın binary'sidir; yeni davranışların kanıtı değildir.

## Kurulum

Public release alanındaki güncel dosyalarla tek komut:

```sh
curl -fsSL https://bp.tunapro.xyz/install.sh | sh -s -- --local
```

Installer uygun binary'yi indirip SHA-256 doğrular; `~/.local/bin/bp` ve Bash/Zsh
başlangıç dosyasına otomatik CLI wrapper'ları kurar. Mevcut dosyalar yedeklenir;
tekrarlı kurulum source satırını çoğaltmaz. Mevcut aliaslar silinmez:
`alias claude="claude --dangerously-skip-permissions"` gibi tanımlar seçenekleriyle
bp üzerinden çalışır. Mevcut özel shell fonksiyonları korunur; mutlak executable
yolu veya `command claude` kullanan aliaslar kendi davranışını koruyarak bp'yi
atlar. Eski sürüm aliası çalışan shell'den kaldırmışsa güncelleme sonrası yeni
terminal açılmalı; rc dosyasındaki alias tanımı silinmemiştir. Yeni terminalde `codex`, `claude`,
`opencode`, `hermes` bp içinde açılır. Aynı terminalde hemen etkinleştirmek için:

```sh
. "$HOME/.config/bp/shell.sh"
```

Tmux kurulu değilse mevcut Homebrew veya Debian/Ubuntu apt kullanılır. Diğer
paket yöneticilerinde önce tmux kurulmalı. Agent CLI'larının kurulumu/girişi bp'den
ayrıdır. Global tmux scroll/prefix/clipboard ayarlarına dokunulmaz.

Yayın öncesi paket kopyasıyla yerel/offline kurulum da mümkün:

```sh
BP_LOCAL_BINARY=/absolute/path/to/bp-for-your-platform sh install.sh --local
```

Bu komut laptopta çalıştırılmalı; sunucu kurulumuyla yerel kurulumu karıştırmamak
için bp'nin legacy sunucu konumunda `bp setup/run` reddedilir.

## Test kanıtı

- `go test -race ./...` bütün Go paketlerinde başarılı; `go vet ./...` başarılı.
- 17 Python testi başarılı: kurulum/tekrarlı setup, Bash wrapper üzerinden açılış,
  dış shell'e dönüş, Ctrl-C iptal/çıkış ayrımı, diğer oturumun korunması,
  dört CLI için çıkış, busy kuyruğun teslimi, Vim, migration ve bekçi kullanımı.
- Vim teslimatı Claude/Codex Normal ve Insert için gerçek tmux soketinde,
  model çağırmayan sahte terminallerle doğrulandı. Türkçe çok satırlı metin tek
  mesaj olarak alındı. Go harness'ları ayrıca paste chip, taslak, modal, klavye
  aktivitesi ve Vim arama alanını kapsar.
- Escape ile paste'i bitirip gönderen etiketini değiştirme/erken Enter saldırıları
  iki CLI'ın iki Vim modunda reddedildi; normal mesaj sonrasında tek kez alındı.
  Go testleri unsafe legacy queue/spool, force ve cleanup yollarını da kapsar.
- Codex'in New chat / Agent command center / Resume menüsünde tuş gönderilmez;
  ilk Enter sonrası bu menüye geçiş teslim başarısı sayılmaz. Menü ve native
  queue hint'i çakışması da [regresyon kapsamındadır](codex-delivery-2026-09-05.md).
- Linux amd64 çalıştırıldı. Diğer üç hedef cross-compile edildi; gerçek macOS
  terminali ve gerçek Claude/Codex modeline teslim bu testlerin kapsamı değildir.
- İnceleme kitaplarıyla aday `bp whoami`, mevcut blueprint çağrısını doğru
  `blueprint / codex-thread / authority=true` olarak doğruladı. Canlı mesaj gönderilmedi.

## Yayın sınırı

Önce [kimlik düzeltmesinin etkisi](identity-fix.md) incelenmeli: kayıt/proof olmayan
Codex çağrıları kesin agent adı ve hiyerarşi yetkisi alamaz; yalnız AGENT bildiren
WhatsApp bridge force çağrıları reddedilir. Bu sınırlar aynı UID/root altında
adversarial izolasyon garantisi değildir.

Public installer/binary/checksum seti site alanına birlikte alındı; eski dosyalar
public alanın dışında yedeklendi. Untracked `install` korundu. Host binary/agentbook geçişinin ayrı adımları
[CODEX-CUTOVER.md](../CODEX-CUTOVER.md) dosyasında. Ortak Codex daemon'u ve çalışan
agentlar restart edilmez. Yetki/P2P tasarımı ve tmux kaydırma Tuna'nın karar
listesinde kalır.
