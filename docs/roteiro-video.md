# Roteiro do vídeo de apresentação — Hackathon FIAP X

O PDF pede um vídeo de **no máximo 10 minutos** cobrindo três coisas, nesta ordem:
documentação, arquitetura escolhida e o projeto funcionando. Este roteiro respeita essa ordem
e reserva metade do tempo para a demonstração, que é a parte que não pode ser lida.

O documento que sustenta a narração é [`entrega-hackathon.md`](entrega-hackathon.md) — cada
requisito do PDF já está mapeado lá, com o ponto do repositório que o atende.

---

## Orçamento de tempo

| Tempo | Duração | Bloco |
|---|---|---|
| 0:00 – 0:40 | 40s | Abertura: o problema e o que o sistema entrega |
| 0:40 – 2:00 | 1:20 | **Documentação** |
| 2:00 – 4:15 | 2:15 | **Arquitetura escolhida** |
| 4:15 – 9:10 | 4:55 | **O projeto funcionando** |
| 9:10 – 9:50 | 40s | Fechamento: checklist de requisitos |

Sobram 10 segundos de folga contra o teto de 10 minutos. Ela é o orçamento para um tropeço,
não para conteúdo adicional — se a demonstração estourar, o corte está definido mais abaixo.

---

## 0:00 – 0:40 · Abertura

Tela: a página inicial da aplicação, já aberta.

> A FIAP X tinha um projeto que processava um vídeo e devolvia os frames num zip, sem nenhuma
> prática de arquitetura. O que vocês vão ver é a versão que os investidores pediram: usuários
> enviam vídeos, acompanham o processamento e baixam o resultado — em uma arquitetura que
> processa vários vídeos ao mesmo tempo, não perde requisição em pico, e avisa o usuário
> quando algo falha.

Dizer, sem detalhar ainda: **cinco processos, três contextos delimitados, uma imagem**.

---

## 0:40 – 2:00 · Documentação

Tela: o repositório no GitHub, depois o editor.

1. **README** (~20s) — ponto de entrada: quickstart em quatro comandos, tabela de rotas,
   tabela de recursos de infraestrutura.
2. **`docs/entrega-hackathon.md`** (~25s) — mostrar a tabela requisito → evidência e dizer
   que ela é o índice da avaliação. *Este é o slide mais importante do bloco.*
3. **`docs/`** (~20s) — passar os olhos pelos seis documentos: arquitetura, modelo de domínio,
   fluxos, desenvolvimento, operações, roadmap.
4. **`openspec/specs/`** (~15s) — 33 especificações de capacidade versionadas junto do código.
   Dizer a frase: *cada mudança entrou com sua especificação, não depois dela.*

Não abrir arquivo por arquivo. Rolar e narrar.

---

## 2:00 – 4:15 · Arquitetura escolhida

Tela: o diagrama Mermaid de [`docs/architecture.md`](architecture.md), renderizado no GitHub.
É a única tela deste bloco — resista a trocar.

Quatro pontos, ~30s cada:

1. **Um serviço por contexto delimitado, atrás de um gateway.** `identity-api`, `video-api` e
   `notification-api` são processos separados; o nginx roteia por prefixo, então o navegador
   continua vendo uma origem só e o front-end não precisou ser tocado quando o monolito foi
   dividido. Cada contexto tem o **seu próprio banco** — o PostgreSQL não consulta entre
   bancos sem extensão, então a fronteira é imposta pelo motor, não por revisão de código.

2. **A fila é o único gatilho.** Apontar para o diagrama: não existe seta de um serviço HTTP
   para o worker. `POST /upload` grava a transição e a linha de outbox na mesma transação e
   responde `202`; um relay publica no RabbitMQ. Sem worker no ar, o upload continua
   funcionando e o job espera. Nada de `ffmpeg` dentro de requisição.

3. **Concorrência é número de workers.** Cada worker segura exatamente um job (`prefetch 1`);
   escalar é subir mais processos. Três réplicas por padrão na pilha local.

4. **Falha é tratada, não evitada.** Lease no Redis escopado por epoch, escrita terminal
   cercada por epoch e status, um *sweeper* que devolve para a fila o job cujo dono sumiu, e
   dead-letter queue para o que não pode ser resolvido. Mencionar que tudo sobre Redis **falha
   aberto**: uma queda de cache degrada, não derruba.

Fechar o bloco com uma frase sobre observabilidade: log estruturado JSON nos cinco processos,
`/metrics` nos cinco, Prometheus e Grafana na pilha.

---

## 4:15 – 9:10 · O projeto funcionando

**A pilha já tem de estar quente.** `docker compose up --build` a frio é minuto de silêncio na
gravação. Ver o checklist de pré-voo.

### 1. Autenticação — ~30s

Registrar um usuário e entrar. Antes disso, mostrar uma chamada sem token sendo recusada
(uma aba com `GET /api/status` retornando `401` já preparada).

> Requisito: *o sistema deve ser protegido por usuário e senha.*

### 2. Três vídeos ao mesmo tempo — ~70s

Enviar **três vídeos curtos** (10–15s cada) em sequência rápida. Alternar para o terminal com
os logs dos três workers e mostrar as três réplicas processando em paralelo — o campo
`instance` do log distingue uma da outra.

> Requisito: *deve processar mais de um vídeo ao mesmo tempo.*

### 3. Listagem de status e download — ~60s

Voltar à página. Mostrar o *polling* evoluindo sozinho de `queued` para `processing` e
`completed`. Clicar em baixar e explicar em uma frase que a API não serve os bytes: ela emite
uma URL pré-assinada de 5 minutos e o navegador busca o ZIP direto do object storage. Abrir o
ZIP para mostrar os frames.

> Requisito: *listagem de status dos vídeos de um usuário.*

### 4. Pico sem perder requisição — ~75s

O bloco de maior impacto. Sequência:

```bash
docker compose up -d --scale worker=0    # derruba os três workers
```

Enviar dois uploads pela página — ambos respondem imediatamente. Mostrar a interface de
gerenciamento do RabbitMQ com as mensagens acumuladas na fila, e a página reportando `queued`.
Então:

```bash
docker compose up -d --scale worker=3    # religa
```

Mostrar a fila drenando e os jobs concluindo sozinhos.

> Requisito: *em caso de picos, o sistema não deve perder uma requisição.*

### 5. Notificação de erro — ~60s

Na seção "Notificações por e-mail" da página, cadastrar um endereço para o evento de falha.
Provocar uma falha — usar o arquivo preparado para isso (ver pré-voo). Abrir o Mailpit em
<http://127.0.0.1:8025> e mostrar a mensagem de falha chegando.

Mencionar em uma frase que o mesmo evento também entrega por **webhook assinado com
HMAC-SHA256**, e que ausência de preferência significa não inscrito — sem padrão implícito.

> Requisito: *em caso de erro, um usuário pode ser notificado.*

### 6. Monitoramento — ~40s

Grafana em <http://127.0.0.1:3000>, dashboard provisionado a partir de arquivo. Mostrar o
gráfico com o pico de trabalho que acabou de ser gerado nos blocos anteriores.

> Stack recomendada: *Prometheus + Grafana.*

**Este é o bloco de corte.** Se o ensaio estourar o tempo, ele vira 15 segundos de captura de
tela em vez de navegação ao vivo.

---

## 9:10 – 9:50 · Fechamento

Tela: a tabela de requisitos técnicos de `docs/entrega-hackathon.md`, mais a aba de Actions do
GitHub com o verde.

Ler rápido, sem elaborar:

- Persistência: PostgreSQL, um banco por contexto, mais MinIO e Redis.
- Escalabilidade: cinco processos independentes; capacidade escala por réplica de worker.
- GitHub: repositório público, `main` protegida, tudo por pull request.
- Testes: **942 funções de teste**, integração de verdade — PostgreSQL, Redis, MinIO, RabbitMQ
  e `ffmpeg` reais — mais testes que verificam o próprio código-fonte.
- CI/CD: quatro verificações obrigatórias em cada PR; imagem multi-plataforma publicada a cada
  release.

Frase final:

> Publicar não é implantar, e a documentação diz isso — não existe ambiente de destino neste
> trabalho, então o que foi entregue é a parte que existe sem um.

---

## Checklist de pré-voo

Fazer **antes** de apertar o gravador. Nada aqui é opcional.

### Ambiente

- [ ] `make dev-keys` executado; `.env` no lugar.
- [ ] `docker compose up --build` rodado até o fim **antes** da gravação — a pilha quente, sem
      build pendente. Confirmar com `docker compose ps` que os treze serviços estão de pé.
- [ ] Uma volta completa de ensaio já feita na pilha, para que o Grafana tenha série histórica
      e o dashboard não apareça vazio.
- [ ] Usuário de demonstração já registrado (ou registrar ao vivo, se for parte do roteiro —
      decidir e manter).

### Material

- [ ] Três vídeos curtos, 10–15 segundos cada, nomes distintos e legíveis na tela.
- [ ] Um arquivo preparado para **falhar** no bloco 5 (extensão válida, conteúdo que o `ffmpeg`
      recusa). Testar que ele realmente falha — descobrir isso gravando é perder a tomada.
- [ ] Um vídeo maior de reserva, caso os curtos processem rápido demais para mostrar
      paralelismo.

### Telas

- [ ] Abas na ordem do roteiro: aplicação (`:8080`), GitHub, editor, RabbitMQ, Mailpit
      (`:8025`), Grafana (`:3000`).
- [ ] Fonte do terminal aumentada — log JSON em fonte pequena é ilegível em vídeo comprimido.
- [ ] Notificações do sistema operacional silenciadas.
- [ ] Nenhum segredo à vista: `.env` fechado, tokens fora do histórico do terminal visível,
      nenhuma URL pré-assinada lida em voz alta.
- [ ] Actions do GitHub verde no momento da gravação.

### Gravação

- [ ] Teste de áudio de 15 segundos, ouvido de volta antes da tomada real.
- [ ] Cronômetro visível para o apresentador.
- [ ] Um ensaio cronometrado completo. O teto de 10 minutos é rígido; descobrir o estouro na
      edição custa uma regravação inteira.

### Publicação

- [ ] Upload no YouTube como **não listado**.
- [ ] Título e descrição identificando grupo e projeto.
- [ ] Link submetido no portal FIAP.
- [ ] Link testado numa janela anônima — um vídeo privado por engano vale zero.
