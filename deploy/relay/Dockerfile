# portide-relay 镜像：用 scripts/package.sh 打出的 Linux 静态二进制构建，不需要在服务器上编译。
#   tar xzf portide-relay-<版本>-linux-amd64.tar.gz && cd portide-relay-<版本>-linux-amd64
#   docker build -t portide/relay .
#   docker run -d --name portide-relay --restart unless-stopped -p 7443:7443/udp -p 7443:7443/tcp -v portide-relay:/data \
#     -e PORTIDE_RELAY_ADDR=你的服务器地址:7443 portide/relay
FROM scratch
COPY portide-relay /portide-relay
ENV PORTIDE_RELAY_DATA=/data
VOLUME /data
EXPOSE 7443/udp 7443/tcp
ENTRYPOINT ["/portide-relay"]
CMD ["serve"]
