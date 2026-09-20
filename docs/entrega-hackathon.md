# Entrega — Hackathon FIAP X

Este documento existe para uma finalidade só: mapear cada item exigido em
[`project-requirements.pdf`](project-requirements.pdf) para o ponto do repositório que o
atende, de modo que a avaliação não dependa de garimpar. É o único documento da pasta
escrito em português — os demais seguem a política de idioma do projeto (inglês para
código e documentação técnica), e nada foi traduzido retroativamente para produzi-lo.

Repositório: <https://github.com/kakadlec/video-processor> (público)

---

## Entregáveis

| # | Exigido pelo PDF (pág. 4) | Onde está |
|---|---|---|
| 1 | Documentação da arquitetura proposta | [`docs/architecture.md`](architecture.md) — inclui o diagrama do runtime. Complementada por [`domain-model.md`](domain-model.md), [`flows.md`](flows.md), [`operations.md`](operations.md), [`development.md`](development.md) e [`roadmap.md`](roadmap.md) |
| 2 | Script de criação do banco de dados ou de outros recursos utilizados | [`README.md` § Database Schema and Infrastructure Resources](../README.md#database-schema-and-infrastructure-resources) indexa os quatro arquivos SQL e os recursos não-SQL (bucket MinIO, topologias RabbitMQ). Detalhe na seção [Recursos e scripts](#recursos-e-scripts) abaixo |
| 3 | Link do GitHub do(s) projeto(s) | <https://github.com/kakadlec/video-processor> |
| 4 | Vídeo de no máximo 10 minutos | Roteiro e plano de gravação em [`roteiro-video.md`](roteiro-video.md) |

---

## Requisitos funcionais

Os cinco requisitos da página 3, na ordem em que o PDF os lista.

### 1. A nova versão do sistema deve processar mais de um vídeo ao mesmo tempo

Concorrência é **número de processos worker**, não número de requisições. Cada réplica de
`cmd/worker` consome a fila com `prefetch 1` e roda exatamente uma extração por vez, por
projeto — o que torna a escala horizontal e previsível em vez de dependente da carga do
processo HTTP.

- `docker-compose.yml` sobe **três** workers por padrão (`worker.deploy.replicas: 3`), então
  a pilha local já processa três vídeos simultaneamente sem nenhum ajuste.
- `docker compose up --scale worker=N` muda esse número nas duas direções.
- Nenhum `ffmpeg` roda dentro de uma requisição HTTP: `POST /upload` responde `202` assim que
  o job está `queued`, e a latência do upload deixou de acompanhar a duração do vídeo.

**Verificar no vídeo:** três uploads simultâneos e os logs das três réplicas processando em
paralelo.

### 2. Em caso de picos, o sistema não deve perder uma requisição

A aceitação e o enfileiramento são **uma transação**. `POST /upload` grava a transição
`pending → queued` e a linha de outbox `video_job.queued.v2` no mesmo commit; um relay
separado publica essa linha no RabbitMQ e só marca `published_at` depois de um *confirm* que
não veio acompanhado de *return*. Não existe janela em que o job foi aceito e a mensagem se
perdeu.

- **Broker fora do ar não derruba o upload:** `RABBITMQ_URL` é exigido na partida, mas um
  broker alcançável não é — o relay disca em segundo plano com backoff e redeclara a topologia
  a cada conexão. O `202` continua sendo respondido.
- **Sem worker no ar nada se perde:** a fila é durável e os jobs ficam em `queued` até um
  worker subir.
- **Worker que morre no meio do trabalho não trava o job:** cada execução segura um *lease*
  no Redis escopado ao `lease_epoch` do PostgreSQL; um *sweeper* confirma a ausência do lease
  duas vezes e devolve o job para `queued` com o epoch avançado. O detentor anterior não
  consegue sobrescrever o sucessor — a escrita terminal é cercada por epoch e status.
- **Falha transitória do object storage não mata o job:** um erro de `Get`/`Put` que não seja
  "objeto inexistente" devolve o job para `queued` num epoch avançado, limitado por
  `domain.MaxJobRequeues`.
- Toda mensagem que não pode ser resolvida vai para uma *dead-letter queue* em vez de sumir.

**Verificar no vídeo:** derrubar os workers, enviar uploads, mostrar os jobs acumulados na
fila, religar os workers e vê-los drenar.

### 3. O sistema deve ser protegido por usuário e senha

`cmd/identity-api` é um contexto delimitado inteiro dedicado a isso.

- `POST /api/auth/register` e `POST /api/auth/login`; senhas com **bcrypt**
  (`internal/identity/infrastructure/password`).
- Token de acesso **JWT RS256** com `kid` no cabeçalho. Só a Identity assina: a capacidade é
  dividida em `Issuer` e `Verifier` com conjuntos de métodos **disjuntos**, de modo que usar o
  lado errado é erro de compilação, e um teste varre o código-fonte dos outros quatro
  processos para garantir que nenhum deles sequer nomeia o construtor do emissor.
- A chave pública é distribuída por **configuração** e não por JWKS: um token emitido antes de
  a Identity cair continua sendo aceito enquanto ela está fora.
- Toda rota não pública exige `Authorization: Bearer` e é **escopada ao dono** — status,
  download e preferências de notificação só enxergam o que pertence ao usuário autenticado.
- Além da autenticação, cada usuário tem um orçamento de requisições (janela fixa no Redis,
  `429` + `Retry-After`), compartilhado entre todos os serviços em vez de um por processo.

**Verificar no vídeo:** registro, login e uma chamada sem token sendo recusada.

### 4. O fluxo deve ter uma listagem de status dos vídeos de um usuário

Duas superfícies, ambas escopadas ao dono:

| Rota | O que devolve |
|---|---|
| `GET /api/video-jobs` | Todos os jobs do usuário com seu estado no ciclo `pending → queued → processing → completed`/`failed` |
| `GET /api/video-jobs/:id` | Um job específico — é o `status_url` que o `202` do upload devolve |
| `GET /api/status` | Os ZIPs já processados, com tamanho e data lidos do próprio objeto armazenado |

A página embutida em `GET /` consome essas rotas: depois do `202` ela faz *polling* do
`status_url` (2s inicial, backoff ×1,5, teto de 10s) até o job reportar `completed` ou
`failed`, e então mostra o botão de download. As leituras passam por um cache no Redis
(*cache-aside* com *write-through* ordenado por `(lease_epoch, status)`), com o PostgreSQL
autoritativo em qualquer miss.

**Verificar no vídeo:** a listagem na tela evoluindo sozinha durante o processamento.

### 5. Em caso de erro, um usuário pode ser notificado (e-mail ou outro meio de comunicação)

Atendido por **dois canais**, não um.

- Todo desfecho terminal — `video_job.completed.v1` e `video_job.failed.v1` — é gravado como
  linha de outbox **na mesma transação** da escrita terminal do job, e publicado por um
  segundo relay que vive no worker (o processo que escreve essas linhas), de modo que anunciar
  um resultado não depende de nenhuma réplica de API estar de pé.
- `cmd/notifier` consome essa fila e resolve cada evento contra as preferências do dono,
  registradas em `GET`/`PUT /api/notification-preferences` e identificadas pela tripla
  `(usuário, tipo de evento, canal)`.
- **Canal `email`:** entrega via relay SMTP (`internal/notification/infrastructure/smtp`). A
  pilha local inclui um *mail catcher* (Mailpit, <http://127.0.0.1:8025>) onde a mensagem
  aparece.
- **Canal `webhook`:** requisição assinada com HMAC-SHA256 sobre `<timestamp>.<corpo>`, com o
  timestamp dentro do valor assinado para que uma requisição capturada não seja repetível para
  sempre.
- Ausência de preferência significa **não inscrito** — não há padrão implícito nem *backfill*,
  e um evento só alcança uma preferência criada antes de ele ocorrer.
- A entrega é reclamada por uma única instrução atômica, cercada por um *claim token* reemitido
  a cada concessão, e o orçamento de tentativas é validado na partida do processo: o notifier
  se recusa a subir se o limite de reclamação for menor que o dobro do tempo máximo que uma
  reclamação pode ser segurada.

**Verificar no vídeo:** cadastrar o e-mail nas preferências, provocar uma falha e mostrar a
mensagem chegando no Mailpit.

---

## Requisitos técnicos

### Arquitetura e infraestrutura

| Exigido | Como é atendido |
|---|---|
| O sistema deve persistir os dados | PostgreSQL é autoritativo para tudo. **Um banco por contexto delimitado** (`identity`, `video`, `notification`), em vez de um schema por contexto — o PostgreSQL não tem consulta entre bancos sem extensão, então a fronteira é imposta pelo motor e não por revisão de código. Artefatos (vídeos de origem e ZIPs de resultado) ficam no MinIO; o Redis carrega apenas estado descartável (idempotência, rate limit, cache de status, leases), e toda funcionalidade sobre ele **falha aberta** |
| O sistema deve estar em uma arquitetura que o permita ser escalado | Cinco processos independentes, um por responsabilidade: três serviços HTTP (um por contexto delimitado) atrás de um gateway nginx, mais worker e notifier fora do caminho da requisição. Os HTTP são sem estado e escalam por réplica; a capacidade de processamento escala pelo número de workers; o gateway resolve o upstream por variável a cada requisição, para que recriar um backend não deixe tráfego indo para um endereço morto |
| O projeto deve ser versionado no GitHub | <https://github.com/kakadlec/video-processor>, público. Fluxo por *pull request*: `main` recusa push direto, inclusive para administradores |
| O projeto deve ter testes que garantam a sua qualidade | **942 funções de teste em 146 arquivos**, para 149 arquivos de código não-teste. São testes de integração de verdade — sobem PostgreSQL, Redis, MinIO e RabbitMQ reais, e rodam o `ffmpeg` de verdade. Além deles, há testes que verificam o **código-fonte** e não o comportamento: proibição de vazamento de identificadores em logs e em rótulos de métrica, regras de dependência entre contextos, e a exclusividade da emissão de tokens — propriedades que nenhum teste de comportamento consegue sustentar, porque ele só enxerga os pontos de chamada que existiam quando foi escrito |
| CI/CD da aplicação | GitHub Actions. Quatro verificações obrigatórias em todo PR: `Build & Test` (`go vet` + `go test`), `SAST (gosec)`, `Vulnerability Scan (govulncheck)` e `Container Image Build`. Versionamento e release automatizados por `release-please`; na release a imagem é publicada em `ghcr.io/kakadlec/video-processor` para `linux/amd64` e `linux/arm64`, com a tag de versão **imutável e sem opção de sobrescrita** |

Uma honestidade que vale registrar: **publicar não é implantar.** Não há ambiente de destino
neste trabalho, então o CD entregue é a parte que existe sem um — a imagem é construída por
uma verificação obrigatória em cada PR e publicada a cada release. A documentação diz isso
explicitamente em vez de deixar inferir o contrário.

### Stack tecnológica

O PDF recomenda; a coluna da direita é o que foi entregue.

| Recomendado (pág. 3–4) | Entregue |
|---|---|
| Containers: Docker + Kubernetes **ou** Docker Compose | **Docker Compose.** Uma imagem multi-stage e *non-root* carrega os cinco binários; a pilha local sobe treze serviços — quinze contêineres, contando as três réplicas do worker. O gateway é o único processo da aplicação que publica porta no host |
| Mensageria: RabbitMQ, Apache Kafka ou similar | **RabbitMQ.** Duas topologias separadas — despacho de jobs (`video.jobs.queued.v2`) e eventos terminais (`video.jobs.terminal.events.v1`) — com dead-letter queues, publicação `mandatory` em canal com *confirm*, e redeclaração da topologia a cada conexão pelos dois lados |
| Banco de Dados: PostgreSQL + Redis (cache) | **PostgreSQL + Redis**, exatamente. Três bancos PostgreSQL (um por contexto) e Redis para cache de status, idempotência, rate limit e leases |
| Monitoramento: Prometheus + Grafana, ELK ou similar | **Prometheus + Grafana.** Os cinco processos expõem `/metrics`; a pilha local sobe um Prometheus raspando todos e um Grafana com dashboard provisionado a partir de arquivo (<http://127.0.0.1:3000>, sem login). Todos os processos emitem log estruturado JSON via `log/slog` |
| CI/CD: GitHub Actions | **GitHub Actions**, as quatro verificações acima |

Além do recomendado: **MinIO** (object storage compatível com S3) para os vídeos de origem e
os ZIPs de resultado, com URLs pré-assinadas de 5 minutos tirando a API do caminho dos bytes.

---

## Recursos e scripts

O PDF pede "script de criação do banco de dados **ou de outros recursos utilizados**". Este
sistema cria quase tudo sozinho na partida — não há etapa de runbook para esquecer nem ordem
entre os cinco binários para acertar. O quadro completo:

| Recurso | Declaração | Aplicado por |
|---|---|---|
| Bancos `identity`, `video`, `notification` (+ um de teste cada) | [`docker/postgres-init/create-context-databases.sql`](../docker/postgres-init/create-context-databases.sql) | PostgreSQL, na primeira inicialização do volume |
| Tabela `identity_users` | [`internal/identity/infrastructure/postgres/schema.sql`](../internal/identity/infrastructure/postgres/schema.sql) | `cmd/identity-api` |
| Tabelas `video_jobs`, `video_job_outbox` | [`internal/video/infrastructure/postgres/schema.sql`](../internal/video/infrastructure/postgres/schema.sql) | `cmd/video-api`, `cmd/worker` |
| Tabelas `notification_preferences`, `notification_deliveries` | [`internal/notification/infrastructure/postgres/schema.sql`](../internal/notification/infrastructure/postgres/schema.sql) | `cmd/notification-api`, `cmd/notifier` |
| Bucket MinIO | `storage.EnsureBucket` | `cmd/video-api`, `cmd/worker` |
| Exchanges, filas, bindings e DLQs do RabbitMQ | `messaging.JobDispatchTopology()`, `TerminalEventsTopology()` | Todo produtor e consumidor, redeclarados a cada conexão |

Os três `schema.sql` são DDL puro, embutidos com `go:embed` e idempotentes
(`CREATE TABLE IF NOT EXISTS`), então também podem ser aplicados à mão com `psql -f` contra um
banco que deva ter o schema antes de qualquer processo subir. A migração é serializada por
`pg_advisory_xact_lock` nos contextos `video` e `notification`, para que duas réplicas subindo
juntas contra um banco ainda não migrado não corram para uma violação de unicidade no catálogo.

---

## Como executar

```bash
git clone https://github.com/kakadlec/video-processor.git
cd video-processor
make dev-keys          # gera o par de chaves RS256 local num .env fora do versionamento
docker compose up --build
```

O gateway atende em <http://127.0.0.1:8080>. Três portas adicionais expõem apenas UIs de
inspeção de desenvolvimento, nenhuma delas atrás do gateway: Mailpit em `:8025`, Prometheus em
`:9090` e Grafana em `:3000`.

Instruções completas, variáveis de ambiente e execução dos testes estão em
[`README.md`](../README.md) e [`docs/development.md`](development.md).
