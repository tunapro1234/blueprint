# bp geliştirici deneyimi incelemesi — 8 Eylül 2026

Tuna'nın talebiyle bağımsız subagent salt-okunur kullanıcı incelemesi yaptı.
İncelenen canlı sürüm SHA fe9040e6…; sonraki native metadata düzeltmeleri bu
review'ün dışındadır. Izole test kanıtları /tmp/bp-review-idg_all3 içinde korundu.

## Öncelikli bulgular

1. **P1: sürüm ve güncelleme.** `bp version` yok; kullanıcıya güncelleme bildirimi
   yok. buildinfo revision/hash operatöre yardımcı ama anlaşılır sürüm değil.
   Küçük çözüm: version --json, update --check, günde bir önbellekli kontrol;
   ağ yoksa açılış bloklanmasın. Agentlara kendiliğinden model turu açtıran mesaj
   yerine terminalde tek satır ve makine-okunur güncelleme bilgisi.
2. **P1: tekrar kurulabilir dağıtım.** install.sh ve Makefile release değişebilir
   URL'ler kullanıyor. Binary/checksum yayın aralığı yarışabilir. Çözüm: değişmez
   /releases/<version>/ dizinleri, tüm paketler hazırken atomik latest.json,
   revision ve kısa değişiklik kaydı; installer belirli sürüm seçebilsin.
3. **P1: yayıncı doğrulaması.** Local checksum aynı sunucudan geliyor;
   --client indirmesinde kontrol eksik. Tek indirme/doğrulama yolu ve imzalı
   manifest gerekir. İlk güven anahtarının nereden alındığı belgelenmeli.
   İmza için anahtar saklama/dağıtım kararı ayrıca verilmeli.
4. **P1: yarıda kalan kurulum.** install.sh binary'yi setup öncesinde değiştiriyor;
   setup hata verirse otomatik rollback yok. Önkontrol, geçici kurulum,
   başarısızlıkta yedekten dönüş ve açık sonuç özeti gerekli.
5. **P1: tek doğru quickstart.** Site eski remote/image kurulumunu anlatıyor;
   npm README de laptop akışıyla uyuşmuyor. Kullanıcı yolu: kurulum → yeni
   terminal → Claude/Codex → status/msg → çıkış. Server bakımı ayrı belge.
   README'deki remaining varsayılanı bu ana çalışma sırasında used olarak düzeltildi.
6. **P2: doctor.** PATH/executable, tmux, shell, alias/function, config/book
   yolları ve runtime eşlemesini modelsiz kontrol eden bp doctor --json.
   Desteklenmeyen shell, binary değiştirilmeden önce bildirilmeli.
7. **P2: devre dışı bırakma.** bp setup --disable yalnız bp shell entegrasyonunu
   kaldırmalı; native aliaslar, konuşmalar, kayıtlar ve açık agentlar korunmalı.
   Binary'yi tek başına silmek mevcut wrapper'ları bozabiliyor.
8. **P2: npm ve lisans.** npm/install.js indirme hatasında exit 0 verebiliyor;
   npm sürümü değişmez native sürüme bağlı değil ve setup çalışmıyor.
   Önce tek güvenilir kurulum yolu. npm ancak eşdeğer güvenceyle önerilmeli.
   UNLICENSED kaydı var; lisans seçimi Tuna'nın kararı.

Bozuk agentbook ile setup başarılı oluyor şüphesi doğrulanmadı: gerçek canlı
binary izole HOME'da bozuk JSON için exit 1 verdi. Bu bir bulgu değildir.

## Küçük dağıtılabilir sürüm için sıra

Önce version + değişmez release manifesti + update --check; sonra kurulum
önkontrol/rollback + doctor + disable; aynı sürümde doğru quickstart.
Mevcut alias koruması, YAML doğrulaması, BP_HOME izolasyonu, kayıt koruma ve
kota kullanmayan tmux testleri temel olarak korunmalı. A2A/P2P veya yeni hesap
sistemi bu sürümün önkoşulu değildir. Bu belge review'dür; önerilen özellikler
bu çalışma sırasında uygulanmış sayılmaz.
