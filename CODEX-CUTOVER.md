# 2026-09-05 bp duzeltmesi — yayin notu

**Host kurulumu tamamlandı; [güncel canlı rapor](docs/live-rollout-2026-09-05.md).**
Bu dosyadaki eski aday hash'leri ve devir adımları tarihsel kayıttır.

**Güncel host paketi ve komut: [host rollout](docs/host-rollout-2026-09-05.md).**
Aşağıdaki eski root UUID'si son sorguda `notLoaded` döndü; tarihsel komutu
uygulama. Public paket yayınlandı, host CLI/servis geçişi ayrı bekliyor.

Kaynak duzeltmeleri iki Codex bicimini destekler; filo tasimasi yapmaz.
Testler model cagirmaz. Ortak Codex app-server ve tmux scroll ayarlari degistirilmedi.
Tuna zaman ayirdiginda yetki, P2P ve tmux scroll/copy-mode birlikte ele alinacak:
[bekleyen kararlar](docs/tuna-pending.md).

Asagidaki `dist/bp-codex` eski asama adayidir; laptop/Vim eklerini icermez.
Guncel tum kaynaklari iceren adaylar ve yayin siniri:
[local release](docs/local-release.md). Canliya geciste inceleyip secilen
guncel Linux adayi kullanilmali; eski hash yeni build kaniti sayilmaz.
Ortak runtime schema 2, sağlayıcıya göre kalan kota, eksik thread eşlemelerinin
teslim etkisi ve bekçinin iki dosyalık yayını: [runtime notu](docs/runtime-status.md).

Bu oturumda `/srv/server-main`, `/srv/probot`, `/usr/local/bin` salt-okunur ve
systemd bus erisimi yok. Dolayisiyla canli agentbook/binary/daemon yayini yapilmadi.
Incelenecek agentbook kopyalari `dist/codex-books/` altinda; guncel 99 + 27 kayit
korundu. `server-main` kok, `server-main-claude` onun altinda. Diger alt hiyerarsiler
korunur. Mevcut root remote thread'i:
`01a0617e-29f9-79a3-be66-72ea1dec4718`, cwd `/srv`.
Model/effort/tier bu kayda sabitlenmez; var olan thread'in secimi korunur.

Dogrulama: `go test ./...`, `go vet ./...`, sekiz kritik pakette `go test -race`
ve Python migration testleri gecti. Aday binary, inceleme kitaplariyla salt-okunur
status kontrolunde server-main'i `codex-remote / gpt-6-astra / medium`, blueprint'i
`codex / gpt-6-astra / high` olarak okudu; thread kimlikleri ve kok dogrulandi.
Model cagrisi veya canli mesaj gonderilmedi.

`dist/bp-codex` SHA256:
`42d79dc7028890b1e4a51042a853bea2eeeecd67aedeff2d516203b926a555e4`

Yayin oncesi [kimlik notundaki](docs/identity-fix.md) uyumluluk etkisini incele:
yalniz AGENT=whatsapp kullanan bridge force cagrilari artik reddedilir;
kaniti/pini olmayan Codex ana agentlari hiyerarsi yetkisi alamaz. Bu siniri
AGENT/cwd fallback ekleyerek gevsetme.

Yetkili ana sunucu kabugunda uygulanacak sira (once degisiklikleri incele):

1. Root thread'in hala ayni oldugunu salt-okunur `thread/read` ile dogrula.
2. `systemctl stop blueprint.service` — dispatcher/keepalive durur; WA bridge child'i
   de kisa sure durur. Codex app-server servisine dokunma.
3. `python3 /srv/blueprint/scripts/migrate-codex-book.py --thread 01a0617e-29f9-79a3-be66-72ea1dec4718 --bind blueprint=01a070c4-3f56-7213-b8ad-19fa7d6f5e4d --bind probot-egitim-astra=01a0712d-46d1-7223-b666-e44fb229c782 --apply`
   Kitaplarin `.lock` kilitlerini alir, guncel dosyalari yeniden okur, ikisini de
   `.before-codex-<zaman>` olarak yedekler, sonra atomik dosya degisimi yapar.
   Iki dosya arasinda hata olursa servisi baslatmadan once ikisini kontrol et.
4. Canli `/srv/blueprint/bp` ve `/usr/local/bin/bp` hedefini yedekle; dogrulanan
   `dist/laptop-release/bp-linux-amd64` adayini (guncel checksums.txt ile dogrulayarak)
   atomik rename ile `/srv/blueprint/bp` olarak yerlestir.
   `/usr/local/bin/bp` ayri kopyaysa onu da ayni adayla guncelle; symlink ise hedefini
   koru. `install --yes` kullanma: tmux ayarlarina da dokunabilir.
5. `systemctl start blueprint.service`; `bp service`, `bp tree`, `bp status --json`
   ile kok, runtime, thread, model/effort ve servis sagligini kontrol et.

Geri donus: yalniz blueprint.service'i durdurup ayni yedek setinden iki kitabi ve
binary'leri geri koy; Codex daemonunu/agentlari restart etme. Mesaj/transkript
loglarini silme. Canli mesaj teslimi ancak ayrica izinli bir mesajla dogrulanabilir;
simule harness testleri bunun yerine gecmez.
