# blueprint (`bp`)

`bp`, sunucudaki agent/tmux, mesaj kuyruğu, usage-policy ve WhatsApp istemci
işlerini tek bir bağımsız Go binary'sinde toplar. `bp daemon` mevcut Python/Node
işlerini zamanlayan supervisor modudur; iş mantığını yeniden yazmaz.

Derleme ve doğrulama:

```sh
make check
```

Binary çalışma alanına yazılır: `/srv/blueprint/bp`.

Komut özeti için `bp help` çalıştırın. `blueprint.service` repo kökündedir;
cutover yapılana kadar `/etc` altına kurulmaz veya etkinleştirilmez.
