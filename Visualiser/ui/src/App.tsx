import { useEffect, useRef, useState } from 'react';
import * as PIXI from 'pixi.js';
import './index.css';

interface Robot {
    id: string;
    x: number;
    y: number;
    heading: number;
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
}

interface StatePayload {
    environment?: Environment;
    robots?: Robot[];
}

const LANDMARK_PREFIX = 'landmark:';

function App() {
    const canvasRef = useRef<HTMLCanvasElement>(null);
    const appRef = useRef<PIXI.Application | null>(null);
    const spritesRef = useRef<Map<string, PIXI.Graphics>>(new Map());
    const envGraphicsRef = useRef<PIXI.Graphics | null>(null);

    const [env, setEnv] = useState<Environment>({ width: 1000, height: 1000, obstacles: [] });
    const [robotCount, setRobotCount] = useState(0);
    const [connected, setConnected] = useState(false);

    // Initialize PixiJS
    useEffect(() => {
        if (!canvasRef.current) return;

        const initPixi = async () => {
            const app = new PIXI.Application();
            await app.init({
                canvas: canvasRef.current!,
                width: 800,
                height: 600,
                backgroundColor: 0x111116,
                resolution: window.devicePixelRatio || 1,
                autoDensity: true,
            });

            const backgroundGraphics = new PIXI.Graphics();
            app.stage.addChild(backgroundGraphics);
            envGraphicsRef.current = backgroundGraphics;

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

        ws.onopen = () => {
            setConnected(true);
            console.log('Connected to Visualiser Server');
        };

        ws.onclose = () => {
            setConnected(false);
            console.log('Disconnected from server');
        };

        ws.onmessage = (event) => {
            try {
                const data: StatePayload = JSON.parse(event.data);
                if (data.environment) {
                    setEnv(data.environment);
                    if (appRef.current && envGraphicsRef.current) {
                        updateEnvironment(data.environment, envGraphicsRef.current);
                    }
                }
                if (data.robots && appRef.current) {
                    updateRobots(data.robots, appRef.current);
                    setRobotCount(data.robots.length);
                }
            } catch (err) {
                console.error('Error parsing WebSocket message', err);
            }
        };

        return () => ws.close();
    }, []);

    const updateEnvironment = (environment: Environment, graphics: PIXI.Graphics) => {
        graphics.clear();

        const scaleX = 800 / environment.width;
        const scaleY = 600 / environment.height;

        // Subtle background grid boundary
        graphics.rect(0, 0, 800, 600);
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

    // Update logic
    const updateRobots = (robots: Robot[], app: PIXI.Application) => {
        const sprites = spritesRef.current;

        // Scale coords to canvas (assuming environment 1000x1000 -> 800x600 canvas)
        const scaleX = 800 / env.width;
        const scaleY = 600 / env.height;

        // Track seen IDs to remove dead robots
        const seenIds = new Set<string>();

        robots.forEach((robot) => {
            seenIds.add(robot.id);

            let sprite = sprites.get(robot.id);
            if (!sprite) {
                // Create new robot sprite
                sprite = new PIXI.Graphics();

                // Triangle shape
                sprite.poly([
                    0, -10,
                    -8, 10,
                    8, 10
                ]);
                sprite.fill(0x00FF88);

                app.stage.addChild(sprite);
                sprites.set(robot.id, sprite);
            }

            // Update position and rotation
            sprite.x = robot.x * scaleX;
            sprite.y = robot.y * scaleY;
            sprite.rotation = robot.heading;
        });

        // Clean up vanished robots
        sprites.forEach((sprite, id) => {
            if (!seenIds.has(id)) {
                app.stage.removeChild(sprite);
                sprite.destroy();
                sprites.delete(id);
            }
        });
    };

    return (
        <div className="dashboard-container">
            <header className="header glass">
                <h1>Swarm Visualiser</h1>
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
