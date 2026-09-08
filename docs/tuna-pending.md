# Tuna zaman ayırınca

Bu liste karar bekleyen işleri tutar; aşağıdaki davranışlar bu çalışmada değiştirilmedi.

- **Claude için en fazla iki özet uyandırması — HENÜZ YAZILMAYACAK (5 Eylül):**
  Tuna mevcut hata düzeltmeleri bitince haber bekliyor. Taslak: yalnız Claude,
  context yaklaşık 300k altında, 43 dakika idle olduğunda bp üzerinden en fazla
  iki mesaj. Mesajda cache denmeyecek; Tuna'nın geri dönmesi için son yapılanları
  kronolojik özetlemesi istenecek. Mosh/tmux bağlantısının açık olması yetmez:
  laptop gerçekten uyanık ve kullanımda olmalı, uykuya geçince mesajlar durmalı.
  Uyanıklık kanıtı, iki mesaj sayacının ne zaman sıfırlanacağı ve 300k eşiği Tuna
  ile netleşmeden otomasyon eklenmeyecek.
- **Basit YAML yapılandırma:** Tuna sonraki talimatıyla bunu ayrıca yetkilendirdi;
  [YAML desteği ve laptop kurulumu](configuration.md) eklendi. Yukarıdaki uyandırma
  otomasyonu bu yetkiye dahil edilmedi.

- **Yetki ve prompt injection:** düşük yetkili agentın üst yetkili agenta mesajıyla işlem yaptırması; mesaj kimliği ile işlem yetkisinin ayrılması, insan onayı ve OS izolasyonu. Önce hangi agentların hangi işlemleri yapabileceğini birlikte belirleyeceğiz.
  Terminal kontrol karakterleriyle zarfı değiştirme yolu kaynakta engellendi;
  gövdedeki düz rol/etiket taklidi ayrı konudur. Gövde metninden yetki çıkarmayan,
  doğrulanmış metadata kullanan tasarım bu kararın parçası olacak.
- **Laptop–sunucu debug bağlantısı — ERTELENDİ (5 Eylül):** sunucu bağlantısının YAML ayarlarına alınması, laptop tmux oturumlarını sunucudan görme ve güvenli etkileşim. Tuna ağ/sunucu kurulumunu şimdilik erteledi; port, tünel, peer veya servis kurulmadı.
- **Cihazlar arası haberleşme:** laptop ↔ sunucu ↔ diğer cihazlar; P2P ve sunucu üzerinden aktarım, kolay eşleştirme, çevrimdışı kuyruk ve erişim iptali. [Seçenekler ve öneri](security-and-transport.md).
- **tmux kaydırma / copy-mode:** Tuna'nın gerçek laptop, terminal ve telefon akışında birlikte deneme. Sunucunun prefix'i `Ctrl-a`; global scroll, fare, tuş ve clipboard ayarları değiştirilmedi. CLI içindeki Vim editörü bundan ayrı; onun mesaj teslim testleri eklendi.
