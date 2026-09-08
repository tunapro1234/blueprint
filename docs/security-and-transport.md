# bp: yetki ve cihazlar arası haberleşme

2026-09-05. Tuna'nın isteğiyle araştırma ve seçenek derlemesi; yeni yetki modeli,
ağ bağlantısı, peer, servis veya port bu çalışma kapsamında uygulanmadı.

## Mevcut kodda gördüğümüz sınırlar

Yerel `bp msg` düz metni yukarıya ve yana gönderebiliyor. Slash komutlarının
hiyerarşi kontrolü var; normal dille “şu komutu çalıştır” demek bu kontrolün
kapsamında değil. Mesajı üst yetkili agentın insan talimatı gibi yorumlaması
bir yetki yükseltme yolu oluşturabilir. Bu kod incelemesi bir açık istismar denemesi değildir.

Olaydan önce yerel gönderen kimliği tmux oturumu ve ortamdan çıkarılıyordu.
Bu araştırma sırasında doğrulanan 5 Eylül kimlik olayı için ayrı bir düzeltme
eklendi: [kapsam ve sınırlar](identity-fix.md). Kod artık belirsizliği güçlü
kimliğe çevirmiyor; bu bir OS yetki ayrımı değildir.
Aynı OS kullanıcısındaki süreçler tmux soketine, agentbook'a ve kuyruk dosyalarına
erişebiliyorsa bp kontrolünü aşabilir. Özellikle root altında isim/hiyerarşi tek
başına bir güvenlik sınırı değildir.

Federation peer tokenını doğruluyor; agent adı peer tarafından belirtiliyor.
Bu, cihazı doğrular, o cihaz içindeki her agent için ayrı kimlik kanıtı sağlamaz.
`expose` hedef listesini sınırlar; mesajın içeriğini yetkili talimat yapmaz.
Client tarafındaki boş expose listesi bütün yerel hedeflere izin verir;
hub tarafındaki expose eşleşmesi ayrı uygulanır.

`internal/fed/client.go:Send` yalnız yapılandırılmış hub peer'ine gönderiyor.
Bugünkü kod laptop A → hub → laptop B rotasını desteklemiyor. HTTPS polling,
mevcut kuyruk ve ACK akışları var; doğrudan P2P taşıma yok.

## Yetki için küçükten büyüğe seçenekler

1. Üst yetkili agenta gelen diğer-agent mesajlarını rapor/soru olarak sunmak;
   yürütme yetkisini metinden devralmamak. Kaynak kimliğini ve güven seviyesini
   ayrı alanlarda taşımak. Bu, riski azaltır; prompt yazısıyla garanti oluşturmaz.
   [OpenAI agent safety](https://developers.openai.com/api/docs/guides/agent-builder-safety)
   ve [OWASP agent security](https://cheatsheetseries.owasp.org/cheatsheets/AI_Agent_Security_Cheat_Sheet.html).
2. İşlem yetkisini LLM dışında denetlemek: hedef, işlem, proje ve süreyle sınırlı
   izin; yüksek etkili işlemde bağımsız insan onayı. Mesaj kimliği ve işlem izni
   ayrı olmalı. Bir agentın altına bağlı olmak, onun tüm yetkilerini vermemeli.
   Bu, bp için önerilen tasarım; mevcut uygulama değil.
   [OWASP](https://cheatsheetseries.owasp.org/cheatsheets/AI_Agent_Security_Cheat_Sheet.html),
   [MCP security](https://modelcontextprotocol.io/docs/draft/tutorials/security/security_best_practices).
3. Güçlü sınır gerektiğinde ayrı OS kullanıcıları/container ve tmux/queue'yu sahiplenen
   bir aracı servis. Agentlar denetim dosyalarını ve üst yetkili tmux soketini
   doğrudan değiştirememeli. Bu daha fazla işletim yükü getirir; laptop için
   şimdilik uygulanması istenmedi.

Şifreleme veya imza göndereni doğrulamaya yardımcı olur; gönderenin talimatını
kendiliğinden güvenilir yapmaz. Prompt injection'ın yalnız metin filtresiyle
çözüldüğünü varsaymamak gerekir.
[OWASP prompt injection](https://cheatsheetseries.owasp.org/cheatsheets/LLM_Prompt_Injection_Prevention_Cheat_Sheet.html).

## Taşıma seçenekleri

| Seçenek | P2P / sunucu | bp'ye eklenecek iş | Karşılığı |
|---|---|---|---|
| Mevcut bp federation'ı genişletmek | Kendi sunucun üzerinden | Client→client yönlendirme, kısa ömürlü eşleştirme, cihaz iptali, hedef izinleri | Az bağımlılık; kuyruğumuz ve CLI aynı kalır. NAT/P2P çözmez. |
| Tailscale + bp kuyruğu | Mümkünse doğrudan, değilse şifreli relay | Cihaz girişi, ağ erişim kuralları ve bp uç noktasının bağlanması | Kendi cihazların için daha az geliştirme; ayrı Tailscale kurulumu/girişi gerekir. |
| iroh | QUIC P2P + relay fallback | Go entegrasyonunun prototipi, eşleştirme, teslim/çevrimdışı kuyruk | Ayrı VPN uygulaması olmadan ürüne gömülebilir; bp için daha fazla mühendislik. |
| NATS JetStream | Sunucu/broker | Subject izinleri, kalıcı consumer, ACK ve bp teslim köprüsü | Çevrimdışı/durable mesajlar için güçlü; ek servis ve işletim yükü. P2P değil. |
| A2A adaptörü | Uygulama protokolü | AgentCard, task/message ve auth eşlemesi | Farklı agent sistemleriyle uyumluluk; NAT, relay ve çevrimdışı kuyruk yerine geçmez. |

Tailscale doğrudan bağlantı ve relay arasında geçiş yapar; ağ grants kuralları,
aynı cihazdaki LLM'lerin işlem yetkisini tek başına çözmez.
[Tailscale bağlantılar](https://tailscale.com/docs/reference/connection-types),
[grants](https://tailscale.com/docs/features/access-control/grants).

iroh bağlantı ve relay altyapısını sağlar; kendi relay'ini kullanmak mümkün.
Mesajın çevrimdışı tutulması ve hedef TUI'da gerçekten teslim edildiğinin kanıtı
bp katmanında kalır. Go bağlayıcısının uygunluğu bu araştırmada derlenerek doğrulanmadı.
[iroh FAQ](https://docs.iroh.computer/about/faq),
[kendi relay'i](https://docs.iroh.computer/add-a-relay).

JetStream ACK alınmayan mesajı tekrar teslim edebilir. Bu yüzden sabit mesaj
kimliği ve bp tarafında tekrar önleme gerekir; broker ACK'i modelin mesajı
aldığının kanıtı değildir.
[NATS consumers](https://docs.nats.io/learn/jetstream/pull-consumers),
[subject authorization](https://docs.nats.io/learn/security/authorization).

A2A kimlik doğrulama/authorization çerçevesi ve agent/task alışverişi tanımlar;
izin kararını sunucu uygulaması verir. Tek başına “alt agent üst agenta talimat
veremez” kuralını sağlamaz.
[A2A specification](https://a2a-protocol.org/latest/specification/).

## Tuna için öneri

Önce mevcut bp hub'ını küçük bir client→client aktarım ve eşleştirme akışıyla
genişletmek en az kod ekleyen yol görünüyor. Bu bir tasarım değerlendirmesidir.
Gerçek P2P ayrıca istenirse kendi cihazların için önce Tailscale değerlendirilmeli.
“Ayrı uygulama ve giriş hiç olmasın” koşulu ağır basarsa iroh için sınırlı bir
prototip anlamlı olur. NATS'i filo/teslim gereksinimi büyürse, A2A'yı farklı
agent ürünleriyle birlikte çalışma ihtiyacı oluşursa eklemek uygun.

Karar öncesi netleştirilecekler: yalnız kendi cihazların mı, davetli kullanıcılar
var mı; sunucu mesaj içeriğini görebilir mi; cihaz çevrimdışıyken kuyruk ne kadar
saklanmalı; üst yetkili agentın hangi işlemleri insan onayına bağlı olmalı.
