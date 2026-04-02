import { useEffect, useRef, useState } from 'react';
import * as PIXI from 'pixi.js';
import './index.css';

interface Robot {
    id: string;
    x: number;
    y: number;
    heading: number;
    is_leader?: boolean;
    communication_range?: number;
    in_range_peer_ids?: string[];
}

interface Obstacle {
    id: string;
    x: number;
    y: number;
    width: number;
    height: number;
}

interface Environment {
    width: number;
    height: number;
    obstacles?: Obstacle[];
    is_paused?: boolean;
}

interface ControlAck {
    type?: string;
    ok?: boolean;
    is_paused?: boolean;
    error?: string;
}

interface StatePayload {
    environment?: Environment;
    robots?: Robot[];
    control?: ControlAck;
}

const LANDMARK_PREFIX = 'landmark:';

const hslToHexColor = (h: number, s: number, l: number): number => {
    const sat = s / 100;
    const light = l / 100;
    const chroma = (1 - Math.abs(2 * light - 1)) * sat;
    const huePrime = h / 60;
    const x = chroma * (1 - Math.abs((huePrime % 2) - 1));

    let r = 0;
    let g = 0;
    let b = 0;

    if (huePrime >= 0 && huePrime < 1) {
        r = chroma;
        g = x;
    } else if (huePrime >= 1 && huePrime < 2) {
        r = x;
        g = chroma;
    } else if (huePrime >= 2 && huePrime < 3) {
        g = chroma;
        b = x;
    } else if (huePrime >= 3 && huePrime < 4) {
        g = x;
        b = chroma;
    } else if (huePrime >= 4 && huePrime < 5) {
        r = x;
        b = chroma;
    } else {
        r = chroma;
        b = x;
    }

    const match = light - chroma / 2;
    const toByte = (value: number) => Math.round((value + match) * 255);
    const red = toByte(r);
    const green = toByte(g);
    const blue = toByte(b);
    return (red << 16) | (green << 8) | blue;
};

const colorForRobot = (robotID: string): number => {
    let hash = 2166136261;
    for (let i = 0; i < robotID.length; i++) {
        hash ^= robotID.charCodeAt(i);
        hash = Math.imul(hash, 16777619);
    }

    const hue = (hash >>> 0) % 360;
    return hslToHexColor(hue, 85, 56);
};

function App() {
    const canvasRef = useRef<HTMLCanvasElement>(null);
    const wsRef = useRef<WebSocket | null>(null);
    const appRef = useRef<PIXI.Application | null>(null);
    const spritesRef = useRef<Map<string, PIXI.Graphics>>(new Map());
    const labelsRef = useRef<Map<string, PIXI.Text>>(new Map());
    const envGraphicsRef = useRef<PIXI.Graphics | null>(null);
    const commGraphicsRef = useRef<PIXI.Graphics | null>(null);

    const [env, setEnv] = useState<Environment>({ width: 1000, height: 1000, obstacles: [] });
    const [robotCount, setRobotCount] = useState(0);
    const [connected, setConnected] = useState(false);
    const [isPaused, setIsPaused] = useState(false);
    const [pausePending, setPausePending] = useState(false);
    const [pauseError, setPauseError] = useState<string | null>(null);

    // Initialize PixiJS
    useEffect(() => {
        if (!canvasRef.current) return;

        const initPixi = async () => {
            const app = new PIXI.Application();
            await app.init({
                canvas: canvasRef.current!,
                width: 1000,
                height: 1000,
                backgroundColor: 0x111116,
                resolution: window.devicePixelRatio || 1,
                autoDensity: true,
            });

            const backgroundGraphics = new PIXI.Graphics();
            app.stage.addChild(backgroundGraphics);
            envGraphicsRef.current = backgroundGraphics;

            const communicationGraphics = new PIXI.Graphics();
            app.stage.addChild(communicationGraphics);
            commGraphicsRef.current = communicationGraphics;

            appRef.current = app;
        };

        initPixi();

        return () => {
            if (appRef.current) {
                appRef.current.destroy(true, { children: true });
                appRef.current = null;
            }
        };
    }, []);

    // WebSocket Connection
    useEffect(() => {
        // In production/Docker, window.location.host works.
        // For local UI dev via Vite, fallback to localhost:8080.
        const wsUrl = import.meta.env.DEV ? 'ws://localhost:8080/ws' : `ws://${window.location.host}/ws`;
        const ws = new WebSocket(wsUrl);
        wsRef.current = ws;

        ws.onopen = () => {
            setConnected(true);
            setPauseError(null);
            console.log('Connected to Visualiser Server');
        };

        ws.onclose = () => {
            setConnected(false);
            setPausePending(false);
            console.log('Disconnected from server');
        };

        ws.onmessage = (event) => {
            try {
                const data: StatePayload = JSON.parse(event.data);

                if (data.control?.type === 'set_pause') {
                    setPausePending(false);
                    if (data.control.ok) {
                        setIsPaused(Boolean(data.control.is_paused));
                        setPauseError(null);
                    } else {
                        setPauseError(data.control.error ?? 'Failed to update pause state');
                    }
                }

                if (data.environment) {
                    setEnv(data.environment);
                    setIsPaused(Boolean(data.environment.is_paused));
                    if (appRef.current && envGraphicsRef.current) {
                        updateEnvironment(data.environment, envGraphicsRef.current);
                    }
                }
                if (data.robots && appRef.current) {
                    updateRobots(data.robots, appRef.current);
                    updateCommunicationOverlay(data.robots);
                    setRobotCount(data.robots.length);
                }
            } catch (err) {
                setPausePending(false);
                console.error('Error parsing WebSocket message', err);
            }
        };

        return () => {
            ws.close();
            wsRef.current = null;
        };
    }, []);

    const togglePause = () => {
        const ws = wsRef.current;
        if (!ws || ws.readyState !== WebSocket.OPEN || pausePending) return;

        const nextPaused = !isPaused;
        setPausePending(true);
        setPauseError(null);

        try {
            ws.send(JSON.stringify({ type: 'set_pause', pause: nextPaused }));
        } catch (error) {
            setPausePending(false);
            setPauseError('Failed to send pause command');
            console.error('Error sending pause command', error);
        }
    };

    const updateEnvironment = (environment: Environment, graphics: PIXI.Graphics) => {
        graphics.clear();

        const scaleX = 1000 / environment.width;
        const scaleY = 1000 / environment.height;

        // Subtle background grid boundary
        graphics.rect(0, 0, 1000, 1000);
        graphics.stroke({ width: 2, color: 0x444455 });

        if (environment.obstacles) {
            const staticObstacles = environment.obstacles.filter((obs) => !obs.id.startsWith(LANDMARK_PREFIX));
            const landmarks = environment.obstacles.filter((obs) => obs.id.startsWith(LANDMARK_PREFIX));

            graphics.fill(0xff3366, 0.5); // Walls/obstacles
            staticObstacles.forEach((obs) => {
                graphics.rect(
                    obs.x * scaleX,
                    obs.y * scaleY,
                    obs.width * scaleX,
                    obs.height * scaleY
                );
            });
            graphics.fill();

            // Render landmarks as bright circles so they stand out from walls.
            landmarks.forEach((lm) => {
                const cx = (lm.x + lm.width / 2) * scaleX;
                const cy = (lm.y + lm.height / 2) * scaleY;
                const radius = Math.max(4, (lm.width * scaleX) / 2);

                let color = 0xffcc00; // default landmark color
                if (lm.id.includes(':casualty')) color = 0x00e5ff;
                if (lm.id.includes(':corridor')) color = 0x7dff6d;
                if (lm.id.includes(':obstacle')) color = 0xffa347;

                graphics.circle(cx, cy, radius);
                graphics.fill({ color, alpha: 0.95 });
                graphics.stroke({ width: 1.5, color: 0x111116, alpha: 0.8 });
            });
        }
    };

    const updateCommunicationOverlay = (robots: Robot[]) => {
        const graphics = commGraphicsRef.current;
        if (!graphics) return;

        graphics.clear();

        if (env.width <= 0 || env.height <= 0) return;

        const scaleX = 1000 / env.width;
        const scaleY = 1000 / env.height;
        const robotsById = new Map<string, Robot>(robots.map((robot) => [robot.id, robot]));

        const drawnPairs = new Set<string>();
        robots.forEach((robot) => {
            const peerIds = robot.in_range_peer_ids ?? [];
            peerIds.forEach((peerId) => {
                const peer = robotsById.get(peerId);
                if (!peer) return;

                const lowID = robot.id < peerId ? robot.id : peerId;
                const highID = robot.id < peerId ? peerId : robot.id;
                const pairKey = `${lowID}|${highID}`;
                if (drawnPairs.has(pairKey)) return;

                drawnPairs.add(pairKey);
                const startX = robot.x * scaleX;
                const startY = robot.y * scaleY;
                const endX = peer.x * scaleX;
                const endY = peer.y * scaleY;
                const midX = (startX + endX) / 2;
                const midY = (startY + endY) / 2;

                graphics.moveTo(startX, startY);
                graphics.lineTo(midX, midY);
                graphics.stroke({ width: 2.4, color: colorForRobot(robot.id), alpha: 0.95 });

                graphics.moveTo(midX, midY);
                graphics.lineTo(endX, endY);
                graphics.stroke({ width: 2.4, color: colorForRobot(peer.id), alpha: 0.95 });
            });
        });

        robots.forEach((robot) => {
            const range = robot.communication_range ?? 0;
            if (range <= 0) return;

            const radiusX = range * scaleX;
            const radiusY = range * scaleY;

            graphics.ellipse(robot.x * scaleX, robot.y * scaleY, radiusX, radiusY);
            graphics.stroke({ width: 2.2, color: colorForRobot(robot.id), alpha: 0.78 });
        });
    };

    // Update logic
    const updateRobots = (robots: Robot[], app: PIXI.Application) => {
        const sprites = spritesRef.current;
        const labels = labelsRef.current;

        // Scale coords to canvas (assuming environment 1000x1000 -> 1000x1000 canvas)
        const scaleX = 1000 / env.width;
        const scaleY = 1000 / env.height;

        // Track seen IDs to remove dead robots
        const seenIds = new Set<string>();

        robots.forEach((robot) => {
            seenIds.add(robot.id);

            let sprite = sprites.get(robot.id);
            if (!sprite) {
                // Create new robot sprite
                sprite = new PIXI.Graphics();
                app.stage.addChild(sprite);
                sprites.set(robot.id, sprite);
            }

            let label = labels.get(robot.id);
            if (!label) {
                label = new PIXI.Text({
                    text: '',
                    style: {
                        fontFamily: 'Courier New',
                        fontSize: 14,
                        fontWeight: '700',
                        fill: 0xffffff,
                        stroke: { color: 0x101014, width: 3 },
                    },
                });
                label.anchor.set(0, 0.5);
                app.stage.addChild(label);
                labels.set(robot.id, label);
            }

            sprite.clear();
            if (robot.is_leader) {
                sprite.rect(-9, -9, 18, 18);
            } else {
                sprite.poly([
                    0, -10,
                    -8, 10,
                    8, 10
                ]);
            }
            sprite.fill(colorForRobot(robot.id));

            // Update position and rotation
            sprite.x = robot.x * scaleX;
            sprite.y = robot.y * scaleY;
            sprite.rotation = robot.is_leader ? 0 : robot.heading;

            label.text = robot.id.slice(0, 5);
            label.x = sprite.x + 11;
            label.y = sprite.y + 11;
        });

        // Clean up vanished robots
        sprites.forEach((sprite, id) => {
            if (!seenIds.has(id)) {
                app.stage.removeChild(sprite);
                sprite.destroy();
                sprites.delete(id);

                const label = labels.get(id);
                if (label) {
                    app.stage.removeChild(label);
                    label.destroy();
                    labels.delete(id);
                }
            }
        });
    };

    return (
        <div className="dashboard-container">
            <header className="header glass">
                <h1>Swarm Visualiser</h1>
                <button
                    type="button"
                    className={`pause-button ${isPaused ? 'resume' : 'pause'}`}
                    onClick={togglePause}
                    disabled={!connected || pausePending}
                >
                    {pausePending ? 'Applying...' : isPaused ? 'Resume' : 'Pause'}
                </button>
                <div className="status-indicators">
                    <div className={`status-dot ${connected ? 'connected' : 'disconnected'}`}></div>
                    <span>{connected ? 'Live' : 'Offline'}</span>
                </div>
            </header>

            <main className="main-content">
                <aside className="sidebar glass">
                    <h2>Telemetry</h2>
                    <div className="stat-box">
                        <span className="label">Active Robots</span>
                        <span className="value">{robotCount}</span>
                    </div>
                    <div className="stat-box">
                        <span className="label">Environment</span>
                        <span className="value">{env.width} x {env.height}</span>
                    </div>
                    <div className="stat-box">
                        <span className="label">Simulation</span>
                        <span className={`value ${isPaused ? 'paused-state' : ''}`}>{isPaused ? 'Paused' : 'Running'}</span>
                    </div>
                    {pauseError && <div className="control-error">{pauseError}</div>}
                </aside>

                <section className="simulation-view glass">
                    <div className="canvas-wrapper">
                        <canvas ref={canvasRef} />
                    </div>
                </section>
            </main>
        </div>
    );
}

export default App;
