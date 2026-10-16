# syntax=docker/dockerfile:1
#
# Asterisk 22 LTS 소스 빌드 이미지 (학습용)
#
# - 실무에서도 Asterisk는 소스로 빌드하는 경우가 많다. 어떤 모듈을 넣을지는 menuselect가 정한다.
# - 디버깅 도구(sngrep, tcpdump, sipp)를 같이 넣어서 컨테이너 안에서 바로 쓸 수 있게 했다.
# - 빌드 트리(/usr/src/asterisk)는 지우지 않았다. app_queue.c 같은 소스를 직접 열어볼 수 있다.
FROM debian:bookworm-slim

ARG ASTERISK_VERSION=22.11.0
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8

# 1) 빌드 도구 + Asterisk가 링크하는 라이브러리 + 학습/디버깅 도구
#    - libsrtp2: WebRTC(Stage 3)에 필요한 SRTP
#    - unixodbc/odbc-mariadb: CDR·queue_log를 MySQL로 보낼 때(Stage 4) 필요
#    - sip-tester: SIPp의 데비안 패키지 이름
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential pkg-config ca-certificates wget bzip2 patch \
        libedit-dev libjansson-dev libxml2-dev uuid-dev libsqlite3-dev \
        libssl-dev libsrtp2-dev libcurl4-openssl-dev libncurses-dev \
        unixodbc-dev odbc-mariadb \
        sngrep tcpdump sip-tester \
        procps iproute2 less \
    && rm -rf /var/lib/apt/lists/*

# 2) 소스 받기
WORKDIR /usr/src/asterisk
RUN wget -qO- "https://downloads.asterisk.org/pub/telephony/asterisk/releases/asterisk-${ASTERISK_VERSION}.tar.gz" \
      | tar xz --strip-components=1

# 3) configure → menuselect(모듈 선택) → make → make install
#    - BUILD_NATIVE 끔: 빌드한 CPU 전용 최적화를 빼서 다른 머신에서도 돌게 한다.
#    - 안내멘트·대기음은 G.711 μ-law(ulaw)로 받는다. 소프트폰 코덱과 같아서 코덱 변환(transcoding)이 없다.
RUN ./configure --with-pjproject-bundled \
    && make menuselect.makeopts \
    && menuselect/menuselect \
        --disable BUILD_NATIVE \
        --disable CORE-SOUNDS-EN-GSM --enable CORE-SOUNDS-EN-ULAW \
        --disable MOH-OPSOUND-WAV --enable MOH-OPSOUND-ULAW \
        menuselect.makeopts \
    && make -j"$(nproc)" \
    && make install \
    && ldconfig

# 4) 시간대 데이터 — 로그를 한국 시간으로 보기 위해 (compose의 TZ 환경변수로 바꿀 수 있다)
#    컴파일 단계 뒤에 두어서, 이 줄을 바꿔도 Asterisk를 다시 컴파일하지 않게 했다.
RUN apt-get update && apt-get install -y --no-install-recommends tzdata \
    && rm -rf /var/lib/apt/lists/*

# 5) 전용 계정으로 실행 (asterisk.conf의 runuser/rungroup)
RUN groupadd --system asterisk \
    && useradd --system --gid asterisk --home-dir /var/lib/asterisk --shell /usr/sbin/nologin asterisk \
    && mkdir -p /etc/asterisk-generated /var/run/asterisk \
    && chown -R asterisk:asterisk /var/lib/asterisk /var/spool/asterisk /var/log/asterisk /var/run/asterisk

COPY --chmod=755 entrypoint.sh /usr/local/bin/entrypoint.sh

WORKDIR /
EXPOSE 5060/udp 10000-10099/udp 5038/tcp 8088/tcp
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
