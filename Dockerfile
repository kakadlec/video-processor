# DOCKERFILE SIMPLES (sem boas práticas - propositalmente!)
# Este é um exemplo de como NÃO fazer um Dockerfile

FROM golang:1.21-alpine

# Instalar ffmpeg
RUN apk add --no-cache ffmpeg

# Criar diretório de trabalho
WORKDIR /app

# Copiar arquivos
COPY . .

# Instalar dependências
RUN go mod tidy

# Criar diretórios necessários
RUN mkdir -p uploads outputs temp

# Expor porta
EXPOSE 8080

# Executar aplicação
CMD ["go", "run", "main.go"] 